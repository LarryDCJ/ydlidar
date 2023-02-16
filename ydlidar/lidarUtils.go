// Package ydlidar this lidar outputs bytes in little endian format
package ydlidar

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"go.bug.st/serial"
	"log"
	"math"
	"os"
	"os/signal"
	"syscall"
	"time"
)

var scanPacketHeaderSize = 10

// NewLidar returns a YDLidar object.
func NewLidar(devicePort serial.Port) *YDLidar {
	return &YDLidar{
		SerialPort: devicePort,
		Packets:    make(chan Packet),
		Stop:       make(chan struct{}),
	}
}

func InitAndConnectToDevice(port *string) (*YDLidar, error) {
	var devicePort serial.Port
	var err error

	devicePort, err = GetSerialPort(port)
	if err != nil {
		return nil, err
	}

	err = devicePort.SetReadTimeout(1000 * time.Millisecond)
	if err != nil {
		return nil, err
	}

	lidar := NewLidar(devicePort)
	lidar.SetupCloseHandler()

	time.Sleep(time.Millisecond * 100)

	deviceInfo, err := lidar.DeviceInfo()
	if err != nil {
		return nil, err
	}
	log.Printf(*deviceInfo)

	healthStatus, err := lidar.HealthInfo()
	if err != nil {
		return nil, err
	}
	log.Printf(*healthStatus)

	return lidar, nil
}

// GetSerialPort returns a real serial port connection.
func GetSerialPort(ttyPort *string) (serial.Port, error) {

	// use ttyPort if not nil
	if ttyPort != nil {

		mode := &serial.Mode{
			BaudRate: 230400,          // 230400 baud
			DataBits: 8,               // 8 data bits
			Parity:   serial.NoParity, // No parity
			StopBits: 0,               // 0 == 1 stop bit
		}

		currentPort, err := serial.Open(*ttyPort, mode)
		if err != nil {
			return nil, err
		}

		//err = currentPort.SetDTR(true)
		//if err != nil {
		//	return nil, err
		//}

		log.Printf("Connected to port: %v", ttyPort)

		return currentPort, nil
	}
	// else iterate over ports to get the correct one
	ports, err := serial.GetPortsList()
	if err != nil {
		log.Panic(err)
	}

	if len(ports) == 0 {
		log.Panic(fmt.Errorf("no serial ports found"))
	}

	mode := &serial.Mode{
		BaudRate: 230400,          // 230400 baud
		DataBits: 8,               // 8 data bits
		Parity:   serial.NoParity, // No parity
		StopBits: 0,               // 0 == 1 stop bit
	}

	// use last port TODO: make this more robust, its hacky and not guaranteed to work
	port := ports[len(ports)-1]
	log.Printf("Using port: %s", port)

	currentPort, err := serial.Open(port, mode)

	err = currentPort.SetDTR(true)
	if err != nil {
		return nil, err
	}

	log.Print("Connected to port: ", currentPort)

	return currentPort, nil

	return nil, nil
}

// SetupCloseHandler creates a 'listener' on a new goroutine which will notify the
// program if it receives an interrupt from the OS. We then handle this by calling
// our clean-up procedure and exiting the program.
func (lidar *YDLidar) SetupCloseHandler() {
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

		os.Exit(0)

	}()
}

// SetDTR enables the DTR control for serial which controls the motor enable function.
func (lidar *YDLidar) SetDTR(s bool) {
	err := lidar.SerialPort.SetDTR(s)
	if err != nil {
		return
	}
}

// DeviceInfo returns the version information.
func (lidar *YDLidar) DeviceInfo() (*string, error) {
	if _, err := lidar.SerialPort.Write([]byte{preCommand, deviceInfo}); err != nil {
		return nil, err
	}

	sizeOfMessage, typeCode, mode, err := lidar.readInfoHeader()
	if err != nil {
		return nil, err
	}

	if typeCode != InfoTypeCode {
		return nil, fmt.Errorf("invalid type code. Expected %x, got %v. Mode: %x", HealthTypeCode, typeCode, mode)
	}

	data := make([]byte, sizeOfMessage)
	n, err := lidar.SerialPort.Read(data)

	if byte(n) != sizeOfMessage {
		return nil, fmt.Errorf("device Info: not enough bytes. Expected %v got %v", sizeOfMessage, n)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read serial:%v", err)
	}

	deviceInfo := &DeviceInfo{}

	deviceInfo.Model = data[0]
	copy(deviceInfo.Firmware[:], data[1:3])
	deviceInfo.Hardware = data[3:4][0]
	copy(deviceInfo.Serial[:], data[4:20])

	runes := make([]rune, len(deviceInfo.Serial))
	for i, b := range deviceInfo.Serial {
		runes[i] = rune(b + '0')
	}

	stringDeviceInfo := &DeviceInfoString{}

	if deviceInfo.Model == 15 {
		stringDeviceInfo.Model = "G2"
		stringDeviceInfo.Firmware = fmt.Sprintf("%v.%v", deviceInfo.Firmware[0], deviceInfo.Firmware[1])
		stringDeviceInfo.Hardware = fmt.Sprintf("%v", deviceInfo.Hardware)
		info := fmt.Sprintf("Device Info: Model: %v Hardware Version: %v Firmware Version: %v Serial Number: %v\n", stringDeviceInfo.Model, stringDeviceInfo.Hardware, stringDeviceInfo.Firmware, string(runes))
		return &info, nil
	} else {
		return nil, fmt.Errorf("unknown model: %v", deviceInfo.Model)
	}

}

// HealthInfo returns the lidar status. Returns nil if the lidar is operating optimally.
func (lidar *YDLidar) HealthInfo() (*string, error) {

	if _, err := lidar.SerialPort.Write([]byte{preCommand, healthStatus}); err != nil {
		return nil, err
	}

	sizeOfMessage, typeCode, mode, err := lidar.readInfoHeader()
	if err != nil {
		return nil, err
	}

	if typeCode != HealthTypeCode {
		return nil, fmt.Errorf("invalid type code. Expected %x, got %v. Mode: %x", HealthTypeCode, typeCode, mode)
	}

	data := make([]byte, sizeOfMessage)
	n, err := lidar.SerialPort.Read(data)

	if byte(n) != sizeOfMessage {
		return nil, fmt.Errorf("health Info: not enough bytes. Expected %v got %v", sizeOfMessage, n)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read serial:%v", err)
	}
	if data[0] == 0x01 {
		return nil, fmt.Errorf("device problem. Error Code:%x %x", data[1], data[2])
	}
	if data[0] == 0 {
		healthInfo := "Health Info: Device is operating optimally"
		return &healthInfo, nil
	}

	return nil, nil
}

// readInfoHeader reads and validate header response.
func (lidar *YDLidar) readInfoHeader() (sizeOfMessage byte, typeCode byte, mode byte, err error) {
	header := make([]byte, 7)
	numBytesInHeader, err := lidar.SerialPort.Read(header)

	if err != nil {
		return 0, 0, 0, err
	}

	if numBytesInHeader != 7 {
		err = fmt.Errorf("read Header: not enough bytes reading header. Expected 7 bytes got %v", numBytesInHeader)
		return 0, 0, 0, err
	}

	startSign := int(header[1])<<8 | int(header[0])
	if startSign != 0x5AA5 {
		return 0, 0, 0, fmt.Errorf("invalid header. Expected startSign 0x5AA5 got %x", startSign)
	}

	// sizeOfMessage is the lower 6 bits of the 6th byte
	sizeOfMessage = header[2] & 0x3F
	log.Printf("SIZE OF MESSAGE: %v", sizeOfMessage)

	typeCode = header[6]

	log.Printf("HEADER: %X", header)

	// the last number is the position of the byte eg. "...& 0xC0 >> 6", the 6 means the 6th byte
	responseMode := header[5] & 0xC0 >> 6
	//log.Printf("mode: %v", mode)

	return sizeOfMessage, typeCode, responseMode, nil
}

// StartScan starts up the scanning and data acquisition.
// see startScan for more details.
func (lidar *YDLidar) StartScan() {

	// n is the number of bytes per scan sample for the YDLidar G4 (Check your lidar's datasheet)
	n := 3

	// Send start scanning command to device.
	if _, err := lidar.SerialPort.Write([]byte{preCommand, startScanning}); err != nil {
		lidar.sendErr(fmt.Errorf("failed to start scan: %v", err))
		return
	}

	/////////////////////////////////////////HEADER/////////////////////////////////////////////
	// size of message should be infinite, so we don't use the value here
	_, typeCode, responseMode, err := lidar.readInfoHeader()
	switch {
	case err != nil:
		err = fmt.Errorf("read header failed: %v", err)

	case typeCode != ScanTypeCode: // 0x81
		err = fmt.Errorf("invalid type code. Expected %x, got %X. Mode: %X", ScanTypeCode, typeCode, responseMode)

	case responseMode != ContinuousResponse: // 0x1
		err = fmt.Errorf("expected continuous response mode, got %X", responseMode)
	}

	if typeCode == ScanTypeCode && responseMode == ContinuousResponse {
		log.Print("Scan Command Response: GOOD")
		cycles := 0
		validFrames := 0
		// Start loop to read distance samples.
		for {
			select {
			default:
				cycles++
				log.Printf("revs: %v", cycles)

				/////////////////////HEADER/////////////////////////////////////////////
				var numHeaderBytesReceived int
				var numSampleBytesReceived int

				// The initial scan packet header is 10 bytes.
				rawHeaderData := make([]byte, scanPacketHeaderSize)
				numHeaderBytesReceived, err := lidar.SerialPort.Read(rawHeaderData)
				if err != nil {
					lidar.sendErr(fmt.Errorf("failed to read serial %v", err))
				}

				// if numSampleBytesReceived != 10, log the actual value
				if numHeaderBytesReceived != scanPacketHeaderSize {
					log.Printf("The lidar gave us %v in the header packet. Expected 10.", numHeaderBytesReceived)
					log.Printf("The header packet is: %X ", rawHeaderData)
					continue
				}

				pointCloud := pointCloudHeader{}
				// Unpack the scan packet header into the pointCloudHeader struct.
				if err = binary.Read(bytes.NewBuffer(rawHeaderData), binary.LittleEndian, &pointCloud); err != nil {
					lidar.sendErr(fmt.Errorf("failed to pack struct: %v", err))
					continue
				}

				// extract the pointCloud into a slice of bytes

				// Returns scan data in a human readable format.
				packetHeader, scanningFrequency, dataPacketType, sampleQuantityPackets := lidar.extractScanPacketHeader(pointCloud)

				log.Printf("Chars: %X", packetHeader)

				if packetHeader == 0 && scanningFrequency == 0 && dataPacketType == 0 && sampleQuantityPackets == 0 {
					log.Printf("OTH PACKET, SKIPPING")
					continue
				}

				switch dataPacketType {
				case 0x1:

					//There is only one zero point of data in the zero start data packet. The sampleQuantityPackets is 1. We skip this packet.
					log.Printf("ZERO START DATA PACKET")
					if numSampleBytesReceived != scanPacketHeaderSize {
						log.Printf("not enough bytes in header. Expected %v got %v", scanPacketHeaderSize, numSampleBytesReceived)
					}
					if sampleQuantityPackets != 1 {
						log.Printf("sample quantity should be 1, got %v", sampleQuantityPackets)
					}

					log.Print("Scanning Frequency is invalid in this packet")

				case 0x0:
					// LOOP OVER THE POINT CLOUD SAMPLES
					//The point cloud data packet contains the distance, angle, and luminosity data.
					validFrames++
					log.Printf("POINT CLOUD DATA PACKET FRAME #%v", validFrames)
					if sampleQuantityPackets <= 0 {
						log.Printf("sample quantity is less than 1 with a continuous response, got %v", sampleQuantityPackets)
						continue
					}
					log.Printf("Scanning Frequency: %vHz", scanningFrequency)

					/////////////////////////LUMINOSITY, DISTANCE, AND ANGLES/////////////////////////////////////
					// 3 bytes per sample, ex. If sampleQuantityPackets is 5, then lengthOfSampleData is 15 because there are 5 samples and each sample is 3 bytes.
					lengthOfSampleData := int(sampleQuantityPackets) * 3

					// Make a slice to hold the raw contents, 3 bytes per sample.
					rawSampleData := make([]byte, lengthOfSampleData)
					numSampleBytesReceived, err = lidar.SerialPort.Read(rawSampleData)
					if err != nil {
						log.Print(fmt.Errorf("failed to read serial %v", err))
					}

					// if the lidar didn't provide the data we expected, let us know
					if numSampleBytesReceived != lengthOfSampleData {
						log.Print(fmt.Errorf("incorrect number of bytes received. Expected %v got %v", lengthOfSampleData, numSampleBytesReceived))
					}

					// Unpack the rawSampleData into the individualSampleBytes slice.
					// the outer slice is the number of samples, the inner slice is the number of bytes per sample
					individualSampleBytes := make([]byte, lengthOfSampleData) //
					if err = binary.Read(bytes.NewBuffer(rawSampleData), binary.LittleEndian, &individualSampleBytes); err != nil {
						log.Panic(fmt.Errorf("failed to pack struct: %v", err))
					}

					// Check Scan Packet Type.
					err = checkScanPacket(rawHeaderData, individualSampleBytes, n)
					if err != nil {
						log.Printf(err.Error())
						continue
					}

					samples := make([][]byte, len(individualSampleBytes)/n)

					//////////////////////////////////Intensity Calculations//////////////////////////
					intensities := calculateIntensities(individualSampleBytes, samples, n)
					/////////////////////////////////////////////////////////////////////////////////

					//////////////////////////////////Distance Calculations///////////////////////////
					distances := calculateDistances(individualSampleBytes, samples, n)
					/////////////////////////////////////////////////////////////////////////////////

					//////////////////////////////Angle Calculations//////////////////////////////////
					angles := calculateAngles(distances, pointCloud.StartAngle, pointCloud.EndAngle, sampleQuantityPackets)
					/////////////////////////////////////////////////////////////////////////////////

					// Send the packet to the channel.
					lidar.Packets <- Packet{
						NumDistanceSamples: int(sampleQuantityPackets),
						Angles:             angles,
						Distances:          distances,
						Intensities:        intensities,
						PacketType:         pointCloud.PackageType,
						Error:              err,
					}
				}

			case <-lidar.Stop:
				return
			}

		}
	}

}

func (lidar *YDLidar) extractScanPacketHeader(pointCloud pointCloudHeader) (uint16, uint8, uint8, uint8) {
	packetHeader := pointCloud.PacketHeader

	scanningFrequency := ((pointCloud.PackageType >> 1) & 0x7F) / 10

	dataPacketType := pointCloud.PackageType & 0x01

	sampleQuantity := pointCloud.SampleQuantity

	return packetHeader, scanningFrequency, dataPacketType, sampleQuantity
}

// checkScanPacket validates the type of the packet.
func checkScanPacket(headerData []byte, sampleData []byte, n int) error {
	checkCode := byte(0)

	// Make a slice big enough to hold headerData (minus the check code position) and sampleData.

	// The check code uses a two-byte exclusive OR to verify the
	// current data packet. The check code itself does not participate in
	// XOR operations, and the XOR order is not strictly in byte order.
	C1 := make([]uint16, 1)
	bufferedC1 := bytes.NewBuffer(headerData[0:2])
	err := binary.Read(bufferedC1, binary.LittleEndian, &C1)
	if err != nil {
		return fmt.Errorf("failed to pack header struct: %v", err)
	}

	C2 := make([]uint16, 1)
	bufferedC2 := bytes.NewBuffer(headerData[4:6])
	err = binary.Read(bufferedC2, binary.LittleEndian, &C2)
	if err != nil {
		return fmt.Errorf("failed to pack header struct: %v", err)
	}

	nextToLastC := make([]uint16, 1)
	bufferedNextToLastC := bytes.NewBuffer(headerData[2:4])
	err = binary.Read(bufferedNextToLastC, binary.LittleEndian, &nextToLastC)
	if err != nil {
		return fmt.Errorf("failed to pack header struct: %v", err)
	}

	lastC := make([]uint16, 1)
	bufferedLastC := bytes.NewBuffer(headerData[6:8])
	err = binary.Read(bufferedLastC, binary.LittleEndian, &lastC)
	if err != nil {
		return fmt.Errorf("failed to pack header struct: %v", err)
	}

	// Calculate Xor of all bits.
	for _, B := range C1 { // for each byte in the packet
		// XOR the current byte with the previous XOR
		checkCode ^= byte(B)

		switch B {

		case 0:
			// Check the first byte.
			if B != 0x55AA && B != 0xA55A {
				return fmt.Errorf("error: first byte of packet is not 0x55AA but %x", B)
			} else {
				log.Printf("First byte of header packet XOR is %x! Nice.", B)
			}
		}
	}

	for _, B := range C2 { // for each byte in the packet
		// XOR the current byte with the previous XOR
		checkCode ^= byte(B)
	}

	for _, B := range nextToLastC { // for each byte in the packet
		// XOR the current byte with the previous XOR
		checkCode ^= byte(B)
	}

	for _, B := range lastC { // for each byte in the packet
		// XOR the current byte with the previous XOR
		checkCode ^= byte(B)
	}

	samplePacket := make([]uint16, len(sampleData)/n)
	bufferedSamples := bytes.NewBuffer(sampleData)
	err = binary.Read(bufferedSamples, binary.LittleEndian, &samplePacket)
	if err != nil {
		return fmt.Errorf("failed to pack sample struct: %v", err)
	}
	log.Printf("Length of sample packet to XOR: %v", len(samplePacket))

	// for each byte in the packet
	for i, B := range samplePacket {
		// check if the byte is divisible by 3
		if i%3 == 0 {
			//zero fill the first 8 bits of this byte
			B = B << 8
		}

		// XOR the current byte with the previous XOR
		checkCode ^= byte(B)

	}

	return nil
}

// GetPointCloud returns point-cloud (intensity, dist, angle) from the data packet.
func GetPointCloud(packet Packet) (pointClouds []PointCloudData) {
	// Zero Point packet.
	if packet.PacketType == 1 {
		pointClouds = append(pointClouds,
			PointCloudData{
				Intensity: packet.Intensities[0],
				Angle:     packet.Angles[0],
				Dist:      packet.Distances[0],
			})
		return
	}

	for i := range packet.Distances {
		intensity := packet.Intensities[i]
		dist := packet.Distances[i]
		angle := packet.Angles[i]
		pointClouds = append(pointClouds,
			PointCloudData{
				Intensity: intensity,
				Angle:     angle,
				Dist:      dist,
			})
	}
	return
}

// StopScan stops the lidar scans, flushes the buffers and closes the serial port.
func (lidar *YDLidar) StopScan() error {
	log.Printf("Stopping scan")
	if _, err := lidar.SerialPort.Write([]byte{preCommand, stopScanning}); err != nil {
		return err
	}
	lidar.Stop <- struct{}{}
	err := lidar.SerialPort.ResetOutputBuffer()
	if err != nil {
		return err
	}
	err = lidar.SerialPort.ResetInputBuffer()
	if err != nil {
		return err
	}
	return nil

}

// sendErr sends error on channel with the packet.
func (lidar *YDLidar) sendErr(err error) {
	lidar.Packets <- Packet{
		Error: err,
	}
}

// Reboot soft reboots the lidar.
func (lidar *YDLidar) Reboot() error {
	if _, err := lidar.SerialPort.Write([]byte{preCommand, restartDevice}); err != nil {
		log.Print("Error sending reboot command: ", err)
		return err
	}
	return nil
}

// Close will shut down the connection.
func (lidar *YDLidar) Close() error {
	return lidar.SerialPort.Close()
}

// calculateAngles calculates the angles of the first and last sample.
func calculateAngles(distances []float32, endAngle uint16, startAngle uint16, sampleQuantity uint8) []float32 {

	// angleCorrect calculates the corrected angles for Lidar.
	angleCorrect := func(dist float32) float32 {
		if dist == 0 {
			return 0
		}
		return float32(180 / math.Pi * math.Atan(21.8*(155.3-float64(dist))/(155.3*float64(dist))))
	}

	angles := make([]float32, sampleQuantity)
	angleCorFSA := angleCorrect(distances[0])
	angleCorLSA := angleCorrect(distances[sampleQuantity-1])

	angleFSA := float32(startAngle>>1)/64 + angleCorFSA
	angleLSA := float32(endAngle>>1)/64 + angleCorLSA

	angleDiff := float32(math.Mod(float64(angleLSA-angleFSA), 360))

	for i := 0; i < len(distances); i++ {
		angle := angleDiff/float32(sampleQuantity-1)*float32(i) + angleLSA + angleCorrect(distances[i])
		angles[i] = angle
	}

	return angles

}

// calculateIntensities calculates the strength of the laser.
func calculateIntensities(individualSampleBytes []byte, samples [][]byte, n int) []int {
	// Si represents the number of samples.
	// Split the individualSampleBytes slice into a slice of slices.
	// Each slice is 3 bytes long.
	// The outer slice is the number of samples.
	// The inner slice is the number of bytes per sample.
	intensities := make([]int, len(individualSampleBytes)/n)

	for Si := range samples {
		samples[Si] = individualSampleBytes[Si*n : (Si+1)*n]
		// uint16(samples[Si][0]) means we take the whole first byte of this grouping
		// uint16(samples[Si][1]&0x3) means the low two bits of the 2nd byte.
		intensity := int(samples[Si][0])
		IH := int(samples[Si][1] & 0x3)
		intensities[Si] = intensity + IH*256
	}
	return intensities
}

// calculateDistances calculates the distances.
func calculateDistances(individualSampleBytes []byte, samples [][]byte, n int) []float32 {
	// Si represents the number of samples.
	// Split the individualSampleBytes slice into a slice of slices.
	// Each inner slice is 3 bytes long.
	// The outer slice is the number of samples.
	// The inner slice is the number of bytes per sample.
	distances := make([]float32, len(individualSampleBytes)/n)
	for Si := range samples {
		samples[Si] = individualSampleBytes[Si*n : (Si+1)*n]

		/////////////////////////DISTANCES/////////////////////////////////////
		// Distance𝑖 = left shift bit(Si(3), 6) + right shift bit(Si(2), 2)
		// This variable represents the distance in millimeters.
		// uint16(samples[Si][1]) >> 2 means we take the first/high 6 bits via shifting it 2 bits to the right
		// uint16(samples[Si][2]) << 6 means we take the last/low 2 bits via shifting it 6 bits to the left
		distance := (uint16(samples[Si][2]) << 6) + (uint16(samples[Si][1]) >> 2)
		distances[Si] = float32(distance)
	}
	return distances
}
