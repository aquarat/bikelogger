# Bikelogger

Bikelogger is a simple golang app that logs data from fitness (and especially cycling) sensors via bluetooth low energy/BLE. 
I wanted a cycling data logger for a track bike, which meant I wanted something (1) small and (2) something that wouldn't be distracting.

Bikelogger should work on anything with Linux and Bluez installed (with the associated BLE hardware, of course) but it's specifically targeted towards the Raspberry Pi Zero W and even more so the NanoPi NEO Air (what a mouthful).

## Getting Started

This project is a baby, so it's not really suitable for anyone who can't read Go. I'm only really just getting an idea of how to interact with BLE devices from Go.

### Prerequisites

You'll need Linux, Bluez (the later the better but I'm currently using 5.37) and suitable hardware (bluetooth low energy modem and host platform).

### Installing

go get github.com/aquarat/bikelogger

### Host Hardware

I'm mostly experimenting with the [FriendlyArm NanoPi NEO](https://www.friendlyarm.com/index.php?route=product/product&path=69&product_id=151) and [Raspberry Pi Zero W](https://www.raspberrypi.org/products/raspberry-pi-zero-w/).
The FriendlyArm NanoPi NEO Air is particularly cool because it uses a moden (and cheap) SoC and it's TINY. It has onboard storage and costs $28.

