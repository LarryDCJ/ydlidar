/*
* Package ydlidar provides go api for YDLidar G2.
* https://www.robotshop.com/media/files/content/y/ydl/pdf/ydlidar_x4_development_manual.pdf
 */
package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"log"
	"math"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.bug.st/serial"
)

const (
	// preCommand is the command to send before sending any other command.
	preCommand = 0xA5 // verified

	// healthStatus is the command to get the health status.
	healthStatus = 0x92 // verified

	// deviceInfo is the command to get the device information.
	deviceInfo = 0x90 // verified

	// resetDevice is the command to reset the device.
	restartDevice = 0x40 // verified

	// stopScanning is the command to stop scanning.
	stopScanning = 0x65 // verified

	// startScanning is the command to start scanning.
	startScanning = 0x60 // verified

	// HealthTypeCode is the device response Health HealthStatus type code.
	HealthTypeCode = 0x06 //verified

	// InfoTypeCode is the device response Device Information type code.
	InfoTypeCode = 0x04 //verified

	// ScanTypeCode is the device response Scan Command type code.
	ScanTypeCode = 0x81 // verified
)

// YDLidar is the lidar object.
type YDLidar struct {
	SerialPort serial.Port
	Packets    chan Packet
	Stop       chan struct{}
}

// PointCloud represents a single lidar reading.
type PointCloud struct {
	Angle float32
	Dist  float32
}

// Packet represents struct of a single sample set of readings.
type Packet struct {
	MinAngle           float32   // Minimum angle corresponds to first distance sample.
	MaxAngle           float32   // Max angle corresponds to last distance sample.
	DeltaAngle         float32   // Delta between Min and Max Angles.
	NumDistanceSamples int       // Number of distance samples.
	Distances          []float32 // Slice containing distance data.
	PacketType         int       // Indicates the current packet type. 0x00: Point cloud packet 0x01: Zero packet.
	Error              error
}

// DeviceInfo Works with G2
// DeviceInfo contains the device model, firmware, hardware, and serial number.
type DeviceInfo struct {
	Model    byte     // Model number.
	Firmware [2]byte  // Firmware version.
	Hardware byte     // Hardware version.
	Serial   [16]byte // Serial number.
}

// DeviceInfoString Works with G2
// DeviceInfoString contains the device model, firmware, hardware, and serial number.
type DeviceInfoString struct {
	Model    string // Model number.
	Firmware string // Firmware version.
	Hardware string // Hardware version.
	Serial   string // Serial number.
}

// pointCloudHeader is the preamble for the point cloud data.
type pointCloudHeader struct {
	// PacketHeader 2B in length, fixed at 0x55AA, low in front, high in back
	PacketHeader uint16 // PH(2B)

	// FrequencyAndPackageType F(bit7:1): represents the scanning frequency of the lidar at the current moment,
	// the value is valid in the initial data packet, and the value is 0 by default in the
	// point cloud data packet; C(bit0): represents the type of the current data packet;
	// 0x00: Point cloud data package 0x01: Start data package
	FrequencyAndPackageType uint8 // F&C (1B)

	// SamplingQuality Indicates the number of sampling points contained in the current packet. There is only one zero point of data in the zero packet. The value is 1.
	SamplingQuantity uint8 // LSN(1B)

	// StartAngle The angle data corresponding to the first sample point in the sampled data
	StartAngle uint16 // FSA(2B)

	// EndAngle The angle data corresponding to the last sample point in the sampled data
	EndAngle uint16 // LSA(2B)

	// CheckCode The check code of the current data packet uses a two-byte exclusive OR to check the current data packet.
	CheckCode uint16 // CS(2B)

	// Don't need to parse this data in the header.
	//// SampleData of the system test is the distance data of the sampling point,
	//// and the interference flag is also integrated in the LSB of the Si node
	//SampleData [3]byte // Si(3B)

}

// NewLidar returns a YDLidar object.
func NewLidar() *YDLidar {
	return &YDLidar{
		Packets: make(chan Packet),
		Stop:    make(chan struct{}),
	}
}

// GetSerialPort returns a real serial port connection.
func GetSerialPort(ttyPort *string) (serial.Port, error) {

	ports, err := serial.GetPortsList()
	if err != nil {
		log.Panic(err)
	}

	if len(ports) == 0 {
		log.Panic("No serial ports found!")
	}

	for _, port := range ports {
		mode := &serial.Mode{
			BaudRate: 230400,          // 230400 baud
			DataBits: 8,               // 8 data bits
			Parity:   serial.NoParity, // No parity
			StopBits: 0,               // 1 stop bit
		}

		currentPort, err := serial.Open(port, mode)
		if err != nil {
			continue
		}

		err = currentPort.SetDTR(true)
		if err != nil {
			return nil, err
		}

		log.Print("Connected to port: ", port)

		return currentPort, nil
	}

	log.Panic("Could not create a connection with the found serial ports!")

	return nil, nil
}

// SetSerial used to set serial.
func (lidar *YDLidar) SetSerial(s serial.Port) {
	lidar.SerialPort = s
}

// StartScan starts up the scanning and data acquisition.
// see startScan for more details.
func (lidar *YDLidar) StartScan() {
	go lidar.startScan()
}

// StopScan TODO: Need to flush the serial buffer when done.
// StopScan stops the lidar scans.
func (lidar *YDLidar) StopScan() error {
	log.Printf("Stopping scan")
	if _, err := lidar.SerialPort.Write([]byte{preCommand, stopScanning}); err != nil {
		return err
	}
	lidar.Stop <- struct{}{}
	return nil

}

// sendErr sends error on channel.
func (lidar *YDLidar) sendErr(e error) {
	lidar.Packets <- Packet{
		Error: e,
	}
}

// SetDTR enables the DTR control for serial which controls the motor enable function.
func (lidar *YDLidar) SetDTR(s bool) error {
	return lidar.SerialPort.SetDTR(s)
}

// startScan runs the data acquisition from the lidar.
func (lidar *YDLidar) startScan() {
	continuousResponse := byte(1)

	if _, err := lidar.SerialPort.Write([]byte{preCommand, startScanning}); err != nil {
		lidar.sendErr(fmt.Errorf("failed to start scan:%v", err))
		return
	}

	// Read and validate header.
	err, _, typeCode, responseMode := readHeader(lidar.SerialPort)
	log.Print(typeCode, "\n", responseMode)
	switch {
	case err != nil:
		err = fmt.Errorf("read header failed: %v", err)

	case typeCode != ScanTypeCode:
		err = fmt.Errorf("invalid type code. Expected %x, got %v. Mode: %x", ScanTypeCode, typeCode, responseMode)

	case responseMode != continuousResponse:
		err = fmt.Errorf("expected continuous response mode, got %v", responseMode)
	}
	if err != nil {
		lidar.sendErr(err)
		return
	}

	// Start loop to read distance samples.
	for {
		select {
		case <-lidar.Stop:
			return
		default:
			// Read point cloud preamble header.
			print("Reading header\n")
			header := make([]byte, 10)
			n, err := lidar.SerialPort.Read(header)
			if byte(n) != 10 {
				lidar.sendErr(fmt.Errorf("not enough bytes. Expected %v got %v", 10, n))
				continue
			}
			if err != nil {
				lidar.sendErr(fmt.Errorf("failed to read serial %v", err))
				continue
			}

			if byte(n) != 10 {
				lidar.sendErr(fmt.Errorf("not enough bytes. Expected %v got %v", 10, n))
				continue
			}

			pointCloudHeader := pointCloudHeader{}

			buf := bytes.NewBuffer(header)
			if err = binary.Read(buf, binary.LittleEndian, &pointCloudHeader); err != nil {
				lidar.sendErr(fmt.Errorf("failed to pack struct: %v", err))
				continue
			}

			// Read distance data.
			data := make([]byte, int(pointCloudHeader.SamplingQuantity)*2)
			n, err = lidar.SerialPort.Read(data)
			if err != nil {
				lidar.sendErr(fmt.Errorf("failed to read serial %v", err))
				continue
			}

			if n != int(pointCloudHeader.SamplingQuantity*2) {
				lidar.sendErr(fmt.Errorf("not enough bytes. Expected %v got %v", pointCloudHeader.SamplingQuantity*2, n))
				continue
			}

			readings := make([]int16, pointCloudHeader.SamplingQuantity)
			buf = bytes.NewBuffer(data)
			if err = binary.Read(buf, binary.LittleEndian, &readings); err != nil {
				lidar.sendErr(fmt.Errorf("failed to pack struct: %v", err))
				continue
			}
			log.Printf("READINGS??? %v \n \n", readings)

			//--------------------------- Everything above this works for the G2 ---------------------------

			// Check CRC of the packet.
			err = checkCRC(header, data, pointCloudHeader.CheckCode)
			if err != nil {
				log.Printf(err.Error())
				continue
			}
			// Check for sane number of packets.
			if pointCloudHeader.SamplingQuantity <= 0 {
				continue
			}

			// Convert readings to millimeters (divide by 4).
			distances := make([]float32, pointCloudHeader.SamplingQuantity)
			for i := uint8(0); i < pointCloudHeader.SamplingQuantity; i++ {
				distances[i] = float32(readings[i]) / 4
			}
			log.Printf("Distances: %v", distances)

			// Calculate angles.
			angleCorFSA := angleCorrection(distances[0])
			angleFSA := float32(pointCloudHeader.StartAngle>>1)/64 + angleCorFSA

			angleCorLSA := angleCorrection(distances[pointCloudHeader.SamplingQuantity-1])
			angleLSA := float32(pointCloudHeader.EndAngle>>1)/64 + angleCorLSA

			var angleDelta float32

			switch {
			case angleLSA > angleFSA:
				angleDelta = angleLSA - angleFSA
			case angleLSA < angleFSA:
				angleDelta = 360 + angleLSA - angleFSA
			case angleLSA == angleFSA:
				angleDelta = 0
			}

			lidar.Packets <- Packet{
				MinAngle:           angleFSA,
				MaxAngle:           angleLSA,
				NumDistanceSamples: int(pointCloudHeader.SamplingQuantity),
				Distances:          distances,
				DeltaAngle:         angleDelta,
				PacketType:         int(pointCloudHeader.PacketHeader),
			}

			log.Printf("FINISHED PACKET\n")
		}
	}
}

// GetPointCloud returns point-cloud (angle, dist) from the data packet.
func GetPointCloud(packet Packet) (pointClouds []PointCloud) {
	// Zero Point packet.
	if packet.PacketType == 1 {
		pointClouds = append(pointClouds,
			PointCloud{
				Angle: packet.MinAngle,
				Dist:  packet.Distances[0],
			})
		return
	}

	for i := 0; i < packet.NumDistanceSamples; i++ {
		dist := packet.Distances[i]
		angle := packet.DeltaAngle/float32(packet.NumDistanceSamples-1)*float32(i) + packet.MinAngle + angleCorrection(dist)
		pointClouds = append(pointClouds,
			PointCloud{
				Angle: angle,
				Dist:  dist,
			})
	}
	return
}

// checkCRC validates the CRC of the packet.
// https://www.robotshop.com/media/files/content/y/ydl/pdf/ydlidar_x4_development_manual.pdf pg 6.
func checkCRC(header []byte, data []byte, crc uint16) error {

	// Make a 16bit slice big enough to hold header (minus CRC) and data.
	dataPacket := make([]uint16, 4+len(data)/2)

	buffer := bytes.NewBuffer(append(header[:8], data...))
	if err := binary.Read(buffer, binary.LittleEndian, &dataPacket); err != nil {
		return fmt.Errorf("failed to pack struct: %v", err)
	}

	log.Printf("dataPacket: %v\n", dataPacket)

	// Calculate Xor of all bits.
	x := uint16(0)
	for i := 0; i < len(dataPacket); i++ {
		x ^= dataPacket[i]
	}
	if x != crc {
		return fmt.Errorf("CRC failed. Ignoring data packet")
	}
	return nil
}

// angleCorrection calculates the corrected angle for Lidar.
func angleCorrection(dist float32) float32 {
	if dist == 0 {
		return 0
	}
	return float32(180 / math.Pi * math.Atan(21.8*(155.3-float64(dist))/(155.3*float64(dist))))
}

// Close will shut down the connection.
func (lidar *YDLidar) Close() error {
	return lidar.SerialPort.Close()
}

// Reboot soft reboots the lidar.
func (lidar *YDLidar) Reboot() error {
	if _, err := lidar.SerialPort.Write([]byte{preCommand, restartDevice}); err != nil {
		return err
	}
	return nil
}

// DeviceInfo returns the version information.
func (lidar *YDLidar) DeviceInfo() (*string, error) {
	if _, err := lidar.SerialPort.Write([]byte{preCommand, deviceInfo}); err != nil {
		return nil, err
	}

	err, sizeOfMessage, typeCode, mode := readHeader(lidar.SerialPort)
	if err != nil {
		return nil, err
	}

	if typeCode != InfoTypeCode {
		return nil, fmt.Errorf("invalid type code. Expected %x, got %v. Mode: %x", HealthTypeCode, typeCode, mode)
	}

	data := make([]byte, sizeOfMessage)
	n, err := lidar.SerialPort.Read(data)

	if byte(n) != sizeOfMessage {
		return nil, fmt.Errorf("not enough bytes. Expected %v got %v", sizeOfMessage, n)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read serial:%v", err)
	}

	deviceInfo := &DeviceInfo{}

	deviceInfo.Model = data[0]
	copy(deviceInfo.Firmware[:], data[1:3])
	deviceInfo.Hardware = data[3:4][0]
	copy(deviceInfo.Serial[:], data[4:20])

	stringDeviceInfo := &DeviceInfoString{}

	if deviceInfo.Model == 15 {
		stringDeviceInfo.Model = "G2"
		stringDeviceInfo.Firmware = fmt.Sprintf("%v.%v", deviceInfo.Firmware[0], deviceInfo.Firmware[1])
		stringDeviceInfo.Hardware = fmt.Sprintf("%v", deviceInfo.Hardware)
		stringDeviceInfo.Serial = string(deviceInfo.Serial[:])
		info := fmt.Sprintf(" Model: %v Hardware Version: %v Firmware Version: %v Serial Number: %v\n", stringDeviceInfo.Model, stringDeviceInfo.Hardware, stringDeviceInfo.Firmware, stringDeviceInfo.Serial)
		return &info, nil
	} else {
		return nil, fmt.Errorf("unknown model: %v", deviceInfo.Model)
	}

}

// HealthStatus returns the lidar status. Returns nil if the lidar is operating optimally.
func (lidar *YDLidar) HealthStatus() (*string, error) {

	if _, err := lidar.SerialPort.Write([]byte{preCommand, healthStatus}); err != nil {
		return nil, err
	}

	err, sizeOfMessage, typeCode, mode := readHeader(lidar.SerialPort)
	if err != nil {
		return nil, err
	}

	if typeCode != HealthTypeCode {
		return nil, fmt.Errorf("invalid type code. Expected %x, got %v. Mode: %x", HealthTypeCode, typeCode, mode)
	}

	data := make([]byte, sizeOfMessage)
	n, err := lidar.SerialPort.Read(data)

	if byte(n) != sizeOfMessage {
		return nil, fmt.Errorf("not enough bytes. Expected %v got %v", sizeOfMessage, n)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read serial:%v", err)
	}
	if data[0] == 0x01 {
		return nil, fmt.Errorf("device problem. Error Code:%x %x", data[1], data[2])
	}

	if data[0] == 0 {
		aReturn := "HEALTHY"
		return &aReturn, nil
	}

	return nil, nil
}

// readHeader reads the header portion of the response.
func readHeader(serialPort serial.Port) (err error, sizeOfMessage byte, typeCode byte, mode byte) {
	header := make([]byte, 7)
	n, err := serialPort.Read(header)

	if err != nil {
		return err, 0, 0, 0
	}

	if n != 7 {
		err = fmt.Errorf("not enough bytes reading header. Expected 7 bytes got %v", n)
		return
	}

	preamble := int(header[1])<<8 | int(header[0])

	if preamble != 0x5AA5 {
		err = fmt.Errorf("invalid header. Expected preamble 0x5AA5 got %x", preamble)
		return
	}

	sizeOfMessage = header[2]
	typeCode = header[6]
	mode = header[5] & 0xC0 >> 6

	return
}

// SetupCloseHandler creates a 'listener' on a new goroutine which will notify the
// program if it receives an interrupt from the OS. We then handle this by calling
// our clean-up procedure and exiting the program.
func SetupCloseHandler(lidar *YDLidar) {
	c := make(chan os.Signal)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-c
		log.Println("Ctrl+C pressed in Terminal")
		err := lidar.StopScan()
		if err != nil {
			return
		}

		err = lidar.Close()
		if err != nil {
			return
		}

		lidar.Reboot()

		os.Exit(0)

	}()
}

func main() {

	devicePort, err := GetSerialPort(nil)
	if err != nil {
		log.Panic(err)
	}

	devicePort.SetReadTimeout(1000 * time.Millisecond)

	lidar := NewLidar()

	SetupCloseHandler(lidar)

	lidar.SetSerial(devicePort)
	if err := lidar.SetDTR(true); err != nil {
		panic(fmt.Sprintf("failed to set DTR:%v", err))
	}

	deviceInfo, err := lidar.DeviceInfo()
	if err != nil {
		log.Panic(err)
	}
	log.Printf("Device Info: %v", *deviceInfo)

	healthStatus, err := lidar.HealthStatus()
	if err != nil {
		log.Panic(err)
	}
	log.Printf("Health Status: %v", *healthStatus)

	go lidar.StartScan()

	// Doesn't scan without stuff below

	img := image.NewRGBA(image.Rect(0, 0, 2048, 2048))
	out, err := os.Create("./output.jpg")

	DEG2RAD := math.Pi / 180
	mapScale := 8.0
	revolutions := 0
	// Loop to read data from channel and construct image.
	for {
		packet := <-lidar.Packets
		//if packet.Error != nil {
		//	log.Panic(packet.Error)
		//}

		// ZeroPt indicates one revolution of lidar. Update image
		// every 10 revolutions.
		if packet.PacketType == 0 {
			revolutions++
			log.Print("\nREVS: ", revolutions, "\n")
			if revolutions == 10 {
				revolutions = 0

				if err := jpeg.Encode(out, img, &jpeg.Options{Quality: 70}); err != nil {
					fmt.Printf("%v", err)
				}
				img = image.NewRGBA(image.Rect(0, 0, 2048, 2048))
			}
		}

		if packet.PacketType == 1 {
			log.Printf("Continuing: %v\n", packet.PacketType)
			continue
		}

		for _, v := range GetPointCloud(packet) {
			//log.Printf("Getting point cloud: %v", v)

			X := math.Cos(float64(v.Angle)*DEG2RAD) * float64(v.Dist)
			Y := math.Sin(float64(v.Angle)*DEG2RAD) * float64(v.Dist)
			log.Printf("X: %v, Y: %v", X, Y)
			Xocc := int(math.Ceil(X/mapScale)) + 1000
			Yocc := int(math.Ceil(Y/mapScale)) + 1000

			img.Set(Xocc, Yocc, color.RGBA{R: 200, G: 100, B: 200, A: 200})
		}
	}

}
