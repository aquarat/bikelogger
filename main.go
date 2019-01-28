package main

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"github.com/godbus/dbus"
	"github.com/jinzhu/gorm"
	_ "github.com/jinzhu/gorm/dialects/sqlite"
	"github.com/muka/go-bluetooth/api"
	"github.com/muka/go-bluetooth/bluez/profile"
	"github.com/muka/go-bluetooth/linux"
	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
	"image"
	"image/color"
	"log"
	"math"
	"os"

	"periph.io/x/periph/conn"
	"periph.io/x/periph/conn/gpio"
	"periph.io/x/periph/conn/gpio/gpioreg"
	"periph.io/x/periph/conn/physic"
	"periph.io/x/periph/conn/spi"
	"periph.io/x/periph/conn/spi/spireg"
	"periph.io/x/periph/host"
	"runtime/debug"
	"strconv"
	"time"
)

var (
	killChans chan chan bool
	DispatcherInput chan *dbus.Signal
	NewNotify chan NewNotifyPacket
	MainChannel chan map[string]*Sample
	DB *gorm.DB
)

func main() {
	epd := &EPD{}
	epd.Init()
	time.Sleep(time.Minute)

	os.Exit(0)

	NewNotify = make(chan NewNotifyPacket, 100)
	MainChannel = make(chan map[string]*Sample)
	DispatcherInput = make(chan *dbus.Signal, 10)
	killChans = make(chan chan bool, 100)
	defer func() {
		go func() {close(killChans)}()
		for j := range killChans {
			j<-false
		}
	}()
	defer BLECleanup()

	go Dispatcher()

	db, err := gorm.Open("sqlite3", "/dev/shm/test.db")
	if err != nil {
		panic("failed to connect database")
	}
	defer db.Close()
	DB = db

	db.CreateTable(&DBPacket{})
	db.CreateTable(&Error{})
	db.AutoMigrate(&DBPacket{})
	db.AutoMigrate(&Error{})

	hciconfig := linux.HCIConfig{}
	_, err = hciconfig.Up()
	CE(err)
	time.Sleep(time.Second)

	moxy := &MoxySensor{Address: "F5:63:A2:C6:8D:8D",
	}
	go moxy.Init()

	heart := &HeartRateSensor{Address: "00:22:D0:35:CE:A2",
	}
	go heart.Init()

	speed := &SpeedSensor{Address: "D0:3B:59:FA:8F:19",
	}
	go speed.Init()

	for {
		select {
		case packet := (<-MainChannel):
			for k, v := range packet {
				log.Println(k, " : ", v)
				CEM(db.Create(&DBPacket{FieldName: k, Value: v.Value, When: time.Now()}))
			}
			break
		}
	}
}

func CEM(db *gorm.DB) {
	err := db.GetErrors()
	for _, j := range err {
		CE(j)
	}
}

type DBPacket struct {
	gorm.Model
	When time.Time `gorm:"primary_key"`
	FieldName string
	Value float32
}

func Dispatcher() {
	Handlers := make(map[string]func(someBytes []byte))

	for {
		select{
		case pack := (<-NewNotify):
			Handlers[pack.UUID] = pack.Handler
			log.Println("added handler ", pack.UUID)
			break
		case packet := (<-DispatcherInput):
				work := packet.Body[1].(map[string]dbus.Variant)["Value"].Value()
				if work != nil {
					someBytes := work.([]byte)
					if Handlers[string(packet.Path)] != nil {
						go Handlers[string(packet.Path)](someBytes)
					}
				}

			break
		}
	}
}

type NewNotifyPacket struct {
	UUID string
	Handler func(someBytes []byte)
}

func BLECleanup() {
		api.Exit()
}
//	err = api.StartDiscovery()
//	CE(err)
//	time.Sleep(time.Second*time.Duration(10))
//	api.StopDiscovery()

type Character struct {
	Name string
	ID string
	CachedID *profile.GattCharacteristic1
	Processor func(someData []byte)
	Notify bool
	Interval time.Duration
	Parent *MoxySensor
	LastPacket time.Time
}

type Sensor interface {
	Init()
}

type HeartRateSensor struct {
	Address string
	Device *api.Device
	Characs []*Character
	Options map[string]dbus.Variant
	Interval time.Duration
	LastCheck time.Time
}

func DeviceConnect(dev *api.Device) {
	retry := true
	delayTime := 5
	var err error

	for retry {
		defer recovery()
		err = dev.Connect()
		if err != nil {
			CE(err)
			delayTime = delayTime * 10
			if delayTime > 500 {
				delayTime = 500
			}
		} else {
			retry = false
		}

		time.Sleep(time.Second*time.Duration(delayTime))
	}
}

func (a *HeartRateSensor) Init() {
	a.Options = make(map[string]dbus.Variant)
	dev, err := api.GetDeviceByAddress(a.Address)
	if err != nil {
		panic(err)
	}

	if dev == nil {
		panic("Device not found")
	}

	a.Device = dev

	DeviceConnect(dev)
	log.Println("HRM connected")

	a.Characs = make([]*Character, 2)
	a.Characs[0] = &Character{Name: "Battery",
		ID: "/org/bluez/hci0/dev_00_22_D0_35_CE_A2/service0025/char0026",
		Processor: BatteryProcFunc,
		Interval: time.Minute,
	}
	a.Characs[0].Init()

	a.Characs[1] = &Character{Name: "Heart Rate",
		ID: "/org/bluez/hci0/dev_00_22_D0_35_CE_A2/service0010/char0011",
		Notify: true,
		Processor: a.HeartRateProcFunc,
	}
	a.Characs[1].Init()
}

type SpeedSensor struct {
	Address string
	Device *api.Device
	Characs []*Character
	Options map[string]dbus.Variant
	Interval time.Duration
	LastCheck time.Time

	WheelEventTime, CrankEventTime uint16
	MyTime int64
}

func (a *SpeedSensor) Init() {
	a.Options = make(map[string]dbus.Variant)
	dev, err := api.GetDeviceByAddress(a.Address)
	if err != nil {
		panic(err)
	}

	if dev == nil {
		panic("Device not found")
	}

	a.Device = dev

	DeviceConnect(dev)

	a.Characs = make([]*Character, 2)
	a.Characs[0] = &Character{Name: "Battery",
		ID: "/org/bluez/hci0/dev_D0_3B_59_FA_8F_19/service000c/char000d",
		Processor: BatteryProcFunc,
		Interval: time.Minute,
	}
	a.Characs[0].Init()

	a.Characs[1] = &Character{Name: "CSC Measure",
		ID: "/org/bluez/hci0/dev_D0_3B_59_FA_8F_19/service0022/char0023",
		Notify: true,
		Processor: a.SpeedProcFunc,
	}
	a.Characs[1].Init()
}

func (a *SpeedSensor) SpeedProcFunc(someBytes []byte) {
	payload := make(map[string]*Sample)

	log.Println("start of speed proc func")
	log.Println(hex.EncodeToString(someBytes))
		a.MyTime = time.Now().Unix()

		log.Println("initial if for wet ",(someBytes[0] & 1))
	log.Println("secondary if for cet ", (someBytes[0] & (1 << 1)))
		log.Println("length of someBytes ", len(someBytes))

		/*2018/10/19 15:28:37 start of speed proc func
2018/10/19 15:28:37 0119000000b3fd
2018/10/19 15:28:37 initial if for wet  1
2018/10/19 15:28:37 secondary if for cet  0
2018/10/19 15:28:37 length of someBytes  7
2018/10/19 15:28:37 end of speed proc func
*/

		if (someBytes[0] & 1) == 1 && len(someBytes) > 6 {
			// Wheel Rev Data Present
			///data = struct.unpack("<BLH", measurement) // little-endian, unsigned char, unsigned long, unsigned short

			WET := &Sample{Value: float32(binary.LittleEndian.Uint16(someBytes[5:7])), When: time.Now()}
			a.WheelEventTime = binary.LittleEndian.Uint16(someBytes[5:7])
			log.Println("WET ", WET)

			if uint16(WET.Value) != a.WheelEventTime {
				a.WheelEventTime = uint16(WET.Value)
				payload["WheelEventTime"] = WET
				payload["WheelRevs"] = &Sample{Value: float32(binary.LittleEndian.Uint32(someBytes[1:5])), When: time.Now()}
				payload["WheelTimeElapsed"] = &Sample{Value: float32(binary.LittleEndian.Uint16(someBytes[5:7])-a.WheelEventTime), When: time.Now()}
			}

		} else if (someBytes[0] & (1 << 1)) == 1 && len(someBytes) > 5 {
			// Crank Rev Data Present
			//data = struct.unpack("<BHH", measurement) // little-endian, unsigned char, unsigned short, unsigned short

			CET := &Sample{Value: float32(binary.LittleEndian.Uint16(someBytes[3:5])), When: time.Now()}
			a.CrankEventTime = binary.LittleEndian.Uint16(someBytes[3:5])
			log.Println("CET ", CET)
			if uint16(CET.Value) != a.CrankEventTime {
				a.CrankEventTime = uint16(CET.Value)
				payload["CSCCrankEventTime"] = CET
				payload["CSCCrankRevs"] = &Sample{Value: float32(binary.LittleEndian.Uint16(someBytes[1:3])), When: time.Now()}
				payload["CSCCrankTimeElapsed"] = &Sample{Value: float32(binary.LittleEndian.Uint16(someBytes[1:3])-a.CrankEventTime), When: time.Now()}
			}
		}

		log.Println("end of speed proc func")


	MainChannel <- payload
}

func (a *Character) Init() {
	killChan := make(chan bool, 1)
	if !a.Notify {
		go SyncCharacteristicHandler(a, killChan)
	} else {
		go ASyncCharacteristicHandler(a, killChan)
	}
}

func (a *HeartRateSensor) HeartRateProcFunc(someBytes []byte) {
	payload := make(map[string]*Sample)
	if len(someBytes) == 4 {
		payload["BPM"] = &Sample{Value: float32(someBytes[1]), When: time.Now()}
	}

	MainChannel <- payload
}

func ASyncCharacteristicHandler(ch *Character, killChan chan bool) {
	gattChar, err := profile.NewGattCharacteristic1(ch.ID)
	if err == nil {
		ch.CachedID = gattChar
	} else {
		CE(err)
		if err != nil {
			log.Println(err, " UUID: ", ch.ID)
			return
		}
	}

	NewNotify<- NewNotifyPacket{UUID: ch.ID, Handler:ch.Processor}

	charRegister, err := gattChar.Register()
	CE(err)

	go func() {
		for {
			if ch == nil {
				log.Println("gatt channel is nil")
				debug.PrintStack()
				return
			}

			select {
			case msg := <-charRegister:
					DispatcherInput <- msg
				break
			case <-killChan: return
			}
		}
	}()

	CE(gattChar.StartNotify())
}

func SyncCharacteristicHandler(ch *Character, killChan chan bool) {
	ticky := time.NewTicker(ch.Interval)

	for {
		defer recovery()
		select {
		case <-ticky.C:

			if ch != nil && ch.Parent != nil && ch.Parent.Device != nil {

			if !ch.Parent.Device.IsConnected() {
				CE(ch.Parent.Device.Connect())
			}

			if ch.Parent.Device.IsConnected() {
				if ch.CachedID == nil {
					charr, err := profile.NewGattCharacteristic1(ch.ID)
					if err == nil {
						ch.CachedID = charr
					} else {
						CE(err)
					}
				}

				someBytes, err := ch.CachedID.ReadValue(ch.Parent.Options)
				if err != nil {
					log.Println(err)
				} else {
					ch.Processor(someBytes)
				}
			} else {
				CE(ch.Parent.Device.Connect())
			}

		}

			break
			case <-killChan: return
		}
	}
}

//00:22:D0:35:CE:A2
func MACtoPath(mac string) (path string) {



	return
}

type MoxySensor struct {
	Address string
	Device *api.Device
	Characs []*Character
	Options map[string]dbus.Variant
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

	a.Device = dev

	DeviceConnect(dev)

	a.Characs = make([]*Character, 3)
	a.Characs[0] = &Character{Name: "Battery",
	ID: "/org/bluez/hci0/dev_F5_63_A2_C6_8D_8D/service000b/char000c",
	Processor: BatteryProcFunc,
	Notify: false,
	Interval: time.Minute,
	}
	a.Characs[0].Init()

	a.Characs[1] = &Character{Name: "Muscle Oxygenation",
		ID: "/org/bluez/hci0/dev_F5_63_A2_C6_8D_8D/service001a/char0024",
		Processor: a.HaemoProcFuncCalc,
		Notify: true,
		Parent: a,
	}
	a.Characs[1].Init()

	a.Characs[2] = &Character{Name: "Muscle Oxygenation Raw",
		ID: "/org/bluez/hci0/dev_F5_63_A2_C6_8D_8D/service001a/char0021",
		Processor: a.HaemoProcFuncRaw,
		Notify: true,
		Parent: a,
	}
	a.Characs[2].Init()
}

func BatteryProcFunc(someBytes []byte) {
	payload := make(map[string]*Sample)

	if len(someBytes) == 1 {
		payload["Battery"] = &Sample{}
		payload["Battery"].Value = float32(someBytes[0])
		payload["Battery"].When = time.Now()
	}

	MainChannel <- payload
}

func groupToArray(someBytes []byte, number int) (output [][]byte) {
	output = make([][]byte, len(someBytes)/number)

	for i := range output {
		output[i] = make([]byte, number)

		for j := range output[i] {
			output[i][j] = someBytes[(i*number)+j]
		}
	}

	return
}

func (a *MoxySensor) HaemoProcFuncRaw(someBytes []byte) {
	payload := make(map[string]*Sample)
	MainChannel <- a.HaemoProcFunc(someBytes, payload, "RAW")
}

func (a *MoxySensor) HaemoProcFuncCalc(someBytes []byte) {
	payload := make(map[string]*Sample)

	MainChannel <- a.HaemoProcFunc(someBytes, payload, "")
}

func (a *MoxySensor) HaemoProcFunc(someBytes []byte, payload map[string]*Sample, extra string) (map[string]*Sample) {
	if len(someBytes) != 16 {
		return nil
	}

	preArray := groupToArray(someBytes, 4)
	postArray := make([]float32, 4)

	for i, j := range preArray {
		postArray[i] = math.Float32frombits(binary.LittleEndian.Uint32(j))
	}

	payload[extra + "Muscle1"] = &Sample{Value: postArray[0], When: time.Now()}
	payload[extra + "Muscle2"] = &Sample{Value: postArray[1], When: time.Now()}
	payload[extra + "Muscle3"] = &Sample{Value: postArray[2], When: time.Now()}
	payload[extra + "Muscle4"] = &Sample{Value: postArray[3], When: time.Now()}

	return payload
}

type Sample struct {
	Value float32
	When time.Time
}

type Error struct {
	gorm.Model
	When time.Time
	Stacktrace string
	ErrorMessage string
}

func CE(e error) {
	if e != nil {
		debug.PrintStack()
		log.Println(e)

		DB.Create(&Error{Stacktrace: bytes.NewBuffer(debug.Stack()).String(),
		ErrorMessage: e.Error(),
		When: time.Now(),
		})
    }
}

func recovery() {
	if r := recover(); r != nil {
		CE((r).(error))
	}
}

func SPIHandler() {
	/*    epd = epd2in13b.EPD()
    epd.init()

    # clear the frame buffer
    frame_black = [0xFF] * (epd.width * epd.height / 8)
    frame_red = [0xFF] * (epd.width * epd.height / 8)
	*/




}



type EPD struct {
	RSTPin, DCPin, BUSYPin, CSPin gpio.PinIO
	EPDWidth, EPDHeight, Rotate, Bus, Dev int
	InitComplete bool
	SPIPort conn.Conn
	InterImage *image.RGBA
	frames [][]byte
}

func (e *EPD) Init() {
	if e.InitComplete {
		return
	}
	e.InitComplete = true
	_, err := host.Init()
	CE(err)

	e.EPDWidth = EPD_WIDTH
	e.EPDHeight = EPD_HEIGHT
	e.SetRotate(ROTATE_90)

	p, err := spireg.Open("")
	CE(err)

	e.SPIPort, err = p.Connect(physic.MegaHertz, spi.Mode3, 8)
	CE(err)

	e.RSTPin = gpioreg.ByName(strconv.Itoa(RST_PIN))
	e.RSTPin.Out(gpio.Level(LOW))
	e.DCPin = gpioreg.ByName(strconv.Itoa(DC_PIN))
	e.DCPin.Out(gpio.Level(LOW))
	e.BUSYPin = gpioreg.ByName(strconv.Itoa(BUSY_PIN))
	e.BUSYPin.Out(gpio.Level(LOW))
	e.CSPin = gpioreg.ByName(strconv.Itoa(CS_PIN))
	e.CSPin.Out(gpio.Level(LOW))

	e.Bus = 0
	e.Dev = 0

	e.Reset()
	e.SendCommand(BOOSTER_SOFT_START)
	e.SendData([]byte{0x17, 0x17, 0x17})
	e.SendCommand(POWER_ON)
	e.WaitUntilIdle()
	e.SendCommand(PANEL_SETTING)
	e.SendData([]byte{0x8F})
	e.SendCommand(VCOM_AND_DATA_INTERVAL_SETTING)
	e.SendData([]byte{0x37})
	e.SendCommand(RESOLUTION_SETTING)
	e.SendData([]byte{0x68, 0x00, 0xD4})

	// hardware init done
	e.frames = make([][]byte, 2)
	e.frames[0] = make([]byte, (e.EPDWidth * e.EPDHeight / 8))
	e.frames[1] = make([]byte, (e.EPDWidth * e.EPDHeight / 8))

	e.ClearDisplay()

	/*
    SPI.mode = 0b00*/

	ticker := time.NewTicker(time.Second*time.Duration(10))
	for range ticker.C {
		e.ClearDisplay()
		e.DrawSomeText(time.Now().String(), 0, 0, 20)
		e.DrawSomeText(time.Now().String(), 0, 0, 40)
		e.WriteFrame(e.RenderFrames())
		log.Println("Frame write complete")
	}
}

func (e *EPD) ClearDisplay() {
	for i := range e.frames {
		for j := range e.frames[i] {
			e.frames[i][j] = 0xff
		}
	}

	e.WriteFrame(e.frames)
}

func (e *EPD) DrawSomeText(label string, red, black, size int) {
	col := color.RGBA{uint8(red), uint8(black), 0, 255}
	point := fixed.Point26_6{fixed.Int26_6(size * 64), fixed.Int26_6(size * 64)}

	d := &font.Drawer{
		Dst:  e.InterImage,
		Src:  image.NewUniform(col),
		Face: basicfont.Face7x13,
		Dot:  point,
	}
	d.DrawString(label)
}

func (e *EPD) RenderFrames() (newFrames [][]byte) {
	frames := make([][]byte, 2)
	frames[0] = make([]byte, (e.EPDWidth * e.EPDHeight / 8))
	frames[1] = make([]byte, (e.EPDWidth * e.EPDHeight / 8))

	X := e.InterImage.Bounds().Max.X
	Y := e.InterImage.Bounds().Max.Y

	for x := 0; x < X; x++ {
		for y := 0; y < Y; y++ {
			pixel := e.InterImage.At(x, y)
			r, g, _, _ := pixel.RGBA()
			setPixelInArray(frames[0], y*EPD_HEIGHT+x, r)
			setPixelInArray(frames[1], y*EPD_HEIGHT+x, g)
		}
	}

	return frames
}

func setPixelInArray(dst []byte, index int, value uint32) {
	rem := uint(math.Mod(float64(index), float64(8)))
	reducedIndex := index / 8
	if value < 100 {
		dst[reducedIndex] = clearBit(dst[reducedIndex], rem)
	} else {
		dst[reducedIndex] = setBit(dst[reducedIndex], rem)
	}
}

func setBit(n byte, pos uint) byte {
	n |= (1 << pos)
	return n
}

func clearBit(n byte, pos uint) byte {
	nn := uint(n)
	mask := uint(^(1 << pos))
	nn &= mask
	return byte(nn)
}


func (e *EPD) SetRotate(rotation int) {
	e.Rotate = rotation

	switch (rotation) {
	case ROTATE_0:
		e.EPDWidth = EPD_WIDTH
		e.EPDHeight = EPD_HEIGHT
		break
	case ROTATE_90:
		e.EPDWidth = EPD_HEIGHT
		e.EPDHeight = EPD_WIDTH
		break
	case ROTATE_180:
		e.EPDWidth = EPD_WIDTH
		e.EPDHeight = EPD_HEIGHT
		break
	case ROTATE_270:
		e.EPDWidth = EPD_HEIGHT
		e.EPDHeight = EPD_WIDTH
		break
	}

	e.InterImage = image.NewRGBA(image.Rectangle{Min: image.Point{X:0, Y:0},
	Max: image.Point{X: e.EPDWidth, Y: e.EPDHeight},
	},
	) // we're just going to use the R and G
}

func (e *EPD) WriteFrame(frames [][]byte) {
	e.SendCommand(DATA_START_TRANSMISSION_1)
	time.Sleep(time.Millisecond*time.Duration(2))
	e.SendData(frames[0])
	time.Sleep(time.Millisecond*time.Duration(2))
	e.SendCommand(DATA_START_TRANSMISSION_2)
	time.Sleep(time.Millisecond*time.Duration(2))
	e.SendData(frames[1])
	time.Sleep(time.Millisecond*time.Duration(2))
	e.SendCommand(DISPLAY_REFRESH)
	e.WaitUntilIdle()
}

func (e *EPD) WaitUntilIdle() {
	for e.DigitalRead(e.BUSYPin) {
		time.Sleep(time.Millisecond*time.Duration(100))
	}
}

func (e *EPD) DigitalRead(pin gpio.PinIO) (state bool) {
	return bool(pin.Read())
}

func (e *EPD) Reset() {
	e.DigitalWrite(e.RSTPin, LOW)
	time.Sleep(time.Duration(200) * time.Millisecond)
	e.DigitalWrite(e.RSTPin, HIGH)
	time.Sleep(time.Duration(200) * time.Millisecond)
}

func (e *EPD) SendCommand(payload byte) {
	e.DigitalWrite(e.DCPin, LOW)
	e.Write([]byte{payload})
}

func (e *EPD) SendData(payload []byte) {
	e.DigitalWrite(e.DCPin, HIGH)
	e.Write(payload)
}

func (e *EPD) Write(payload []byte) []byte {
	aBuffer := make([]byte, len(payload))
	if len(payload) > 4000 {
		aBuffer = make([]byte, 0)
		segments := len(payload)/4000

		for i := 0; i < segments; i++ {
			mBuff := make([]byte, 4000)
			CE(e.SPIPort.Tx(payload[i*4000:(i+1)*4000], mBuff))
			aBuffer = append(aBuffer, mBuff...)
		}
		mBuff := make([]byte, 4000)
		CE(e.SPIPort.Tx(payload[segments*4000:], mBuff))
		aBuffer = append(aBuffer, mBuff...)
	} else {
		CE(e.SPIPort.Tx(payload, aBuffer))
	}

	return aBuffer
}

func (e *EPD) DigitalWrite(pin gpio.PinIO, newState bool) {
	CE(pin.Out(gpio.Level(newState) ))
}



const (
	RST_PIN         = 17
	DC_PIN          = 25
	CS_PIN          = 8
	BUSY_PIN        = 24

	LOW bool = false
	HIGH bool = true

// Display resolution
EPD_WIDTH       = 104
EPD_HEIGHT      = 212

// EPD2IN13B commands
PANEL_SETTING                               = 0x00
POWER_SETTING                               = 0x01
POWER_OFF                                   = 0x02
POWER_OFF_SEQUENCE_SETTING                  = 0x03
POWER_ON                                    = 0x04
POWER_ON_MEASURE                            = 0x05
BOOSTER_SOFT_START                          = 0x06
DEEP_SLEEP                                  = 0x07
DATA_START_TRANSMISSION_1                   = 0x10
DATA_STOP                                   = 0x11
DISPLAY_REFRESH                             = 0x12
DATA_START_TRANSMISSION_2                   = 0x13
VCOM_LUT                                    = 0x20
W2W_LUT                                     = 0x21
B2W_LUT                                     = 0x22
W2B_LUT                                     = 0x23
B2B_LUT                                     = 0x24
PLL_CONTROL                                 = 0x30
TEMPERATURE_SENSOR_CALIBRATION              = 0x40
TEMPERATURE_SENSOR_SELECTION                = 0x41
TEMPERATURE_SENSOR_WRITE                    = 0x42
TEMPERATURE_SENSOR_READ                     = 0x43
VCOM_AND_DATA_INTERVAL_SETTING              = 0x50
LOW_POWER_DETECTION                         = 0x51
TCON_SETTING                                = 0x60
RESOLUTION_SETTING                          = 0x61
GET_STATUS                                  = 0x71
AUTO_MEASURE_VCOM                           = 0x80
VCOM_VALUE                                  = 0x81
VCM_DC_SETTING_REGISTER                     = 0x82
PARTIAL_WINDOW                              = 0x90
PARTIAL_IN                                  = 0x91
PARTIAL_OUT                                 = 0x92
PROGRAM_MODE                                = 0xA0
ACTIVE_PROGRAM                              = 0xA1
READ_OTP_DATA                               = 0xA2
POWER_SAVING                                = 0xE3

	ROTATE_0                                    = 0
	ROTATE_90                                   = 1
	ROTATE_180                                  = 2
	ROTATE_270                                  = 3
)

/*
[NEW] Characteristic
        /org/bluez/hci0/dev_00_22_D0_35_CE_A2/service000c/char000d
        00002a05-0000-1000-8000-00805f9b34fb
        Service Changed

[NEW] Characteristic
        /org/bluez/hci0/dev_00_22_D0_35_CE_A2/service0010/char0011
        00002a37-0000-1000-8000-00805f9b34fb
        Heart Rate Measurement
-> NOT PERMITTED
--> notify works

[NEW] Characteristic
        /org/bluez/hci0/dev_00_22_D0_35_CE_A2/service0010/char0014
        00002a38-0000-1000-8000-00805f9b34fb
        Body Sensor Location
-> 0x01

[NEW] Characteristic
        /org/bluez/hci0/dev_00_22_D0_35_CE_A2/service0016/char0017
        00002a23-0000-1000-8000-00805f9b34fb
        System ID
[NEW] Characteristic
        /org/bluez/hci0/dev_00_22_D0_35_CE_A2/service0016/char0019
        00002a24-0000-1000-8000-00805f9b34fb
        Model Number String
-> H7\x00

[NEW] Characteristic
        /org/bluez/hci0/dev_00_22_D0_35_CE_A2/service0016/char001b
        00002a25-0000-1000-8000-00805f9b34fb
        Serial Number String
[NEW] Characteristic
        /org/bluez/hci0/dev_00_22_D0_35_CE_A2/service0016/char001d
        00002a26-0000-1000-8000-00805f9b34fb
        Firmware Revision String
[NEW] Characteristic
        /org/bluez/hci0/dev_00_22_D0_35_CE_A2/service0016/char001f
        00002a27-0000-1000-8000-00805f9b34fb
        Hardware Revision String
[NEW] Characteristic
        /org/bluez/hci0/dev_00_22_D0_35_CE_A2/service0016/char0021
        00002a28-0000-1000-8000-00805f9b34fb
        Software Revision String
[NEW] Characteristic
        /org/bluez/hci0/dev_00_22_D0_35_CE_A2/service0016/char0023
        00002a29-0000-1000-8000-00805f9b34fb
        Manufacturer Name String
-> Polar Electro Oy

[NEW] Characteristic
        /org/bluez/hci0/dev_00_22_D0_35_CE_A2/service0025/char0026
        00002a19-0000-1000-8000-00805f9b34fb
        Battery Level
-> 0x5a

[NEW] Characteristic
        /org/bluez/hci0/dev_00_22_D0_35_CE_A2/service0028/char0029
        6217ff4c-c8ec-b1fb-1380-3ad986708e2d
        Vendor specific
[NEW] Characteristic
        /org/bluez/hci0/dev_00_22_D0_35_CE_A2/service0028/char002b
        6217ff4d-91bb-91d0-7e2a-7cd3bda8a1f3
        Vendor specific
 */

 /*
 	sensors := make([]*Sensor, 2)
	senheart := Sensor(heart)
	sensors[0] = &senheart
	senmox := Sensor(moxy)
	sensors[1] = &senmox
  */