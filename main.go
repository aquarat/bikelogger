package main

import (
	"github.com/godbus/dbus"
	"github.com/muka/go-bluetooth/api"
	"github.com/muka/go-bluetooth/bluez/profile"
	"log"
	"runtime/debug"
	"time"
)

func main() {
	moxy := &MoxySensor{Address: "F5:63:A2:C6:8D:8D",
	Interval: time.Second,
	}
	moxy.Init()

	sensors := make([]*Sensor, 1)
	senmox := Sensor(moxy)
	sensors[0] = &senmox

	ticky := time.NewTicker(time.Second/2)

	for range ticky.C {
		for _, j := range sensors {
			jj := *j
			if jj.ShouldGet() {
				sample := jj.GetData()
				log.Println(sample)
			}
		}
	}
}

type Character struct {
	Name string
	ID string
	CachedID *profile.GattCharacteristic1
	Processor func(someData []byte, payload map[string]*Sample) map[string]*Sample
}

type SensorPayload struct {
	Data map[string]float32
}

type Sensor interface {
	Init()
	GetData() *SensorPayload
	ShouldGet() bool
}

type MoxySensor struct {
	Address string
	Device *api.Device
	Characs []*Character
	Options map[string]dbus.Variant
	Interval time.Duration
	LastCheck time.Time
}

func BatteryLevelProcessor(someBytes []byte, payload map[string]*Sample) map[string]*Sample {

	return payload
}

func (a *MoxySensor) Init() {
	a.Options = make(map[string]dbus.Variant)
	dev, err := api.GetDeviceByAddress(a.Address)
	if err != nil {
		panic(err)
	}

	if dev == nil {
		panic("Device not found")
	}

	err = dev.Connect()
	if err != nil {
		panic(err)
	}

	a.Characs = make([]*Character, 2)
	a.Characs[0] = &Character{Name: "Battery",
	ID: "00002a19-0000-1000-8000-00805f9b34fb",
	Processor: BatteryLevelProcessor,
	}

}

func (a *MoxySensor) BatteryProcFunc(someBytes []byte, payload map[string]*Sample) (map[string]*Sample) {
	payload["Battery"] = &Sample{}

	if len(someBytes) > 0 {
		payload["Battery"].Value = float32(someBytes[0])
		payload["Battery"].When = time.Now()
	}

	return payload
}

func (a *MoxySensor) HaemoProcFunc() {

}

type Sample struct {
	Value float32
	When time.Time
}

func (a *MoxySensor) ShouldGet() (yes bool) {
	if time.Now().Sub(a.LastCheck) > a.Interval {
		a.LastCheck = time.Now()
		yes = true
	}

	return
}

func (a *MoxySensor) GetData() *SensorPayload {
	payload := make(map[string]*Sample)

	if a.Device.IsConnected() {
		for _, p := range a.Characs {
			if p.CachedID == nil {
				charr, err := a.Device.GetCharByUUID(p.ID)
				if err == nil {
					p.CachedID = charr
				} else {
					CE(err)
				}
			}

			someBytes, err := p.CachedID.ReadValue(a.Options)
			if err != nil {
				log.Println(err)
			} else {
				payload = p.Processor(someBytes, payload)
			}
		}
	} else {
		CE(a.Device.Connect())
	}
 return nil
}

func CE(e error) {
	if e != nil {
		debug.PrintStack()
		log.Println(e)
	}
}