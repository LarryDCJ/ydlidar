package main

import (
	"log"
	. "ydlidarg2/ydlidar"
)

func main() {

	// TODO read in from config file with option to remain nil
	var devicePort *string

	lidar, err := InitAndConnectToDevice(devicePort)
	defer lidar.StopScan()

	if err != nil {
		log.Panic(err)
	}

	go lidar.StartScan()

	// Loop to read data from channel
	for {
		packet := <-lidar.Packets
		for _, v := range GetPointCloud(packet) {
			// print the packet
			log.Printf("Angle: %v Dist: %v Intensity: %v", v.Angle, v.Dist, v.Intensity)
		}
	}
}
