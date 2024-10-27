//go:build wioterminal

// memo1 : build with -stack-size 20KB
// memo2 : make .init file(For example:net.init)
// package main
// func init() {
// 	ssid = "???"
// 	password = "???"
// 	openWeatherAPIkey = "???"
// }

package main

import (
	"encoding/json"
	"fmt"
	"image/color"
	"io"
	"log"
	"machine"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"tinygo.org/x/drivers"
	"tinygo.org/x/drivers/netlink"
	"tinygo.org/x/drivers/netlink/probe"
)

// source code citation start position "tinygo.org/x/drivers/hub75" (rev.1bf1a11067968352afa5d7a489a13561effb2146)
type Config struct {
	Width      int16
	Height     int16
	ColorDepth uint16
	RowPattern int16
	Brightness uint8
	FastUpdate bool
}

type Device struct {
	bus               drivers.SPI
	a                 machine.Pin
	b                 machine.Pin
	c                 machine.Pin
	d                 machine.Pin
	oe                machine.Pin
	lat               machine.Pin
	width             int16
	height            int16
	brightness        uint8
	fastUpdate        bool
	colorDepth        uint16
	colorStep         uint16
	colorHalfStep     uint16
	colorThirdStep    uint16
	colorTwoThirdStep uint16
	rowPattern        int16
	rowsPerBuffer     int16
	panelWidth        int16
	panelWidthBytes   int16
	pixelCounter      uint32
	lineCounter       uint32
	patternColorBytes uint8
	rowSetsPerBuffer  uint8
	sendBufferSize    uint16
	rowOffset         []uint32
	buffer            [][]uint8 // [ColorDepth][(width * height * 3(rgb)) / 8]uint8
	displayColor      uint16
}

// New returns a new HUB75 driver. Pass in a fully configured SPI bus.
func New(b drivers.SPI, latPin, oePin, aPin, bPin, cPin, dPin machine.Pin) Device {
	aPin.Configure(machine.PinConfig{Mode: machine.PinOutput})
	bPin.Configure(machine.PinConfig{Mode: machine.PinOutput})
	cPin.Configure(machine.PinConfig{Mode: machine.PinOutput})
	dPin.Configure(machine.PinConfig{Mode: machine.PinOutput})
	oePin.Configure(machine.PinConfig{Mode: machine.PinOutput})
	latPin.Configure(machine.PinConfig{Mode: machine.PinOutput})

	return Device{
		bus: b,
		a:   aPin,
		b:   bPin,
		c:   cPin,
		d:   dPin,
		oe:  oePin,
		lat: latPin,
	}
}

// Configure sets up the device.
func (d *Device) Configure(cfg Config) {
	if cfg.Width != 0 {
		d.width = cfg.Width
	} else {
		d.width = 64
	}
	if cfg.Height != 0 {
		d.height = cfg.Height
	} else {
		d.height = 32
	}
	if cfg.ColorDepth != 0 {
		d.colorDepth = cfg.ColorDepth
	} else {
		d.colorDepth = 8
	}
	if cfg.RowPattern != 0 {
		d.rowPattern = cfg.RowPattern
	} else {
		d.rowPattern = 16
	}
	if cfg.Brightness != 0 {
		d.brightness = cfg.Brightness
	} else {
		d.brightness = 255
	}

	d.fastUpdate = cfg.FastUpdate
	d.rowsPerBuffer = d.height / 2
	d.panelWidth = 1
	d.panelWidthBytes = (d.width / d.panelWidth) / 8
	d.rowOffset = make([]uint32, d.height)
	d.patternColorBytes = uint8((d.height / d.rowPattern) * (d.width / 8))
	d.rowSetsPerBuffer = uint8(d.rowsPerBuffer / d.rowPattern)
	d.sendBufferSize = uint16(d.patternColorBytes) * 3
	d.colorStep = 256 / d.colorDepth
	d.colorHalfStep = d.colorStep / 2
	d.colorThirdStep = d.colorStep / 3
	d.colorTwoThirdStep = 2 * d.colorThirdStep
	d.buffer = make([][]uint8, d.colorDepth)
	for i := range d.buffer {
		d.buffer[i] = make([]uint8, (d.width*d.height*3)/8)
	}

	d.colorHalfStep = d.colorStep / 2
	d.colorThirdStep = d.colorStep / 3
	d.colorTwoThirdStep = 2 * d.colorThirdStep

	d.a.Low()
	d.b.Low()
	d.c.Low()
	d.d.Low()
	d.oe.High()

	var i uint32
	for i = 0; i < uint32(d.height); i++ {
		d.rowOffset[i] = (i%uint32(d.rowPattern))*uint32(d.sendBufferSize) + uint32(d.sendBufferSize) - 1
	}
}

// SetPixel modifies the internal buffer in a single pixel.
func (d *Device) SetPixel(x int16, y int16, c color.RGBA) {
	d.fillMatrixBuffer(x, y, c.R, c.G, c.B)
}

// fillMatrixBuffer modifies a pixel in the internal buffer given position and RGB values
func (d *Device) fillMatrixBuffer(x int16, y int16, r uint8, g uint8, b uint8) {
	if x < 0 || x >= d.width || y < 0 || y >= d.height {
		return
	}
	x = d.width - 1 - x

	var offsetR uint32
	var offsetG uint32
	var offsetB uint32

	vertIndexInBuffer := uint8((int32(y) % int32(d.rowsPerBuffer)) / int32(d.rowPattern))
	whichBuffer := uint8(y / d.rowsPerBuffer)
	xByte := x / 8
	whichPanel := uint8(xByte / d.panelWidthBytes)
	inRowByteOffset := uint8(xByte % d.panelWidthBytes)

	offsetR = d.rowOffset[y] - uint32(inRowByteOffset) - uint32(d.panelWidthBytes)*
		(uint32(d.rowSetsPerBuffer)*(uint32(d.panelWidth)*uint32(whichBuffer)+uint32(whichPanel))+uint32(vertIndexInBuffer))
	offsetG = offsetR - uint32(d.patternColorBytes)
	offsetB = offsetG - uint32(d.patternColorBytes)

	bitSelect := uint8(x % 8)

	for c := uint16(0); c < d.colorDepth; c++ {
		colorTresh := uint8(c*d.colorStep + d.colorHalfStep)
		if r > colorTresh {
			d.buffer[c][offsetR] |= 1 << bitSelect
		} else {
			d.buffer[c][offsetR] &^= 1 << bitSelect
		}
		if g > colorTresh {
			d.buffer[(c+d.colorThirdStep)%d.colorDepth][offsetG] |= 1 << bitSelect
		} else {
			d.buffer[(c+d.colorThirdStep)%d.colorDepth][offsetG] &^= 1 << bitSelect
		}
		if b > colorTresh {
			d.buffer[(c+d.colorTwoThirdStep)%d.colorDepth][offsetB] |= 1 << bitSelect
		} else {
			d.buffer[(c+d.colorTwoThirdStep)%d.colorDepth][offsetB] &^= 1 << bitSelect
		}
	}
}

// Display sends the buffer (if any) to the screen.
func (d *Device) Display() error {
	rp := uint16(d.rowPattern)
	for i := uint16(0); i < rp; i++ {
		// FAST UPDATES (only if brightness = 255)
		if d.fastUpdate && d.brightness == 255 {
			d.setMux((i + rp - 1) % rp)
			d.lat.High()
			d.oe.Low()
			d.lat.Low()
			time.Sleep(1 * time.Microsecond)
			d.bus.Tx(d.buffer[d.displayColor][i*d.sendBufferSize:(i+1)*d.sendBufferSize], nil)
			time.Sleep(10 * time.Microsecond)
			d.oe.High()

		} else { // NO FAST UPDATES
			d.setMux(i)
			d.bus.Tx(d.buffer[d.displayColor][i*d.sendBufferSize:(i+1)*d.sendBufferSize], nil)
			d.latch((255 * uint16(d.brightness)) / 255)
		}
	}
	d.displayColor++
	if d.displayColor >= d.colorDepth {
		d.displayColor = 0
	}
	return nil
}

func (d *Device) latch(showTime uint16) {
	d.lat.High()
	d.lat.Low()
	d.oe.Low()
	time.Sleep(time.Duration(showTime) * time.Microsecond)
	d.oe.High()
}

func (d *Device) setMux(value uint16) {
	if (value & 0x01) == 0x01 {
		d.a.High()
	} else {
		d.a.Low()
	}
	if (value & 0x02) == 0x02 {
		d.b.High()
	} else {
		d.b.Low()
	}
	if (value & 0x04) == 0x04 {
		d.c.High()
	} else {
		d.c.Low()
	}
	if (value & 0x08) == 0x08 {
		d.d.High()
	} else {
		d.d.Low()
	}
}

// FlushDisplay flushes the display
func (d *Device) FlushDisplay() {
	var i uint16
	for i = 0; i < d.sendBufferSize; i++ {
		d.bus.Tx([]byte{0x00}, nil)
	}
}

// SetBrightness changes the brightness of the display
func (d *Device) SetBrightness(brightness uint8) {
	d.brightness = brightness
}

// ClearDisplay erases the internal buffer
func (d *Device) ClearDisplay() {
	bufferSize := (d.width * d.height * 3) / 8
	for c := uint16(0); c < d.colorDepth; c++ {
		for j := int16(0); j < bufferSize; j++ {
			d.buffer[c][j] = 0
		}
	}
}

// Size returns the current size of the display.
func (d *Device) Size() (w, h int16) {
	return d.width, d.height
}

// source code citation end position "tinygo.org/x/drivers/hub75" (rev.1bf1a11067968352afa5d7a489a13561effb2146)

type WeatherJSON struct {
	Timezone       string `json:"timezone"`
	TimezoneOffset int    `json:"timezone_offset"`
	Hourly         [6]struct {
		Dt      int `json:"dt"`
		Weather [1]struct {
			Icon string `json:"icon"`
		} `json:"weather"`
	} `json:"hourly"`
}

type WeatherInfoList struct {
	Date string
	Info []WeatherInfo
}

type WeatherInfo struct {
	Hour string
	ID   string
}

type fontData [5][3]uint8
type iconData [7][7]uint8

var (
	ssid              string
	password          string
	openWeatherAPIkey string

	// 試験的にデータを取得したところ、13554byte だったため、余裕をもって 20000byte 分を確保しておく。
	httpBody = [20000]byte{}
	readBuf  = [512]byte{}
	apiURL   = ""

	fonts = [13]fontData{
		// 0
		fontData{
			{1, 1, 1},
			{1, 0, 1},
			{1, 0, 1},
			{1, 0, 1},
			{1, 1, 1},
		},
		// 1
		fontData{
			{0, 1, 0},
			{1, 1, 0},
			{0, 1, 0},
			{0, 1, 0},
			{0, 1, 0},
		},
		// 2
		fontData{
			{1, 1, 1},
			{1, 0, 1},
			{0, 1, 0},
			{1, 0, 0},
			{1, 1, 1},
		},
		// 3
		fontData{
			{1, 1, 1},
			{0, 0, 1},
			{1, 1, 1},
			{0, 0, 1},
			{1, 1, 1},
		},
		// 4
		fontData{
			{1, 0, 1},
			{1, 0, 1},
			{1, 1, 1},
			{0, 0, 1},
			{0, 0, 1},
		},
		// 5
		fontData{
			{1, 1, 1},
			{1, 0, 0},
			{1, 1, 1},
			{0, 0, 1},
			{1, 1, 1},
		},
		// 6
		fontData{
			{1, 1, 1},
			{1, 0, 0},
			{1, 1, 1},
			{1, 0, 1},
			{1, 1, 1},
		},
		// 7
		fontData{
			{1, 1, 1},
			{1, 0, 1},
			{0, 0, 1},
			{0, 0, 1},
			{0, 0, 1},
		},
		// 8
		fontData{
			{1, 1, 1},
			{1, 0, 1},
			{1, 1, 1},
			{1, 0, 1},
			{1, 1, 1},
		},
		// 9
		fontData{
			{1, 1, 1},
			{1, 0, 1},
			{1, 1, 1},
			{0, 0, 1},
			{1, 1, 1},
		},
		// ":"
		fontData{
			{0, 0, 0},
			{0, 1, 0},
			{0, 0, 0},
			{0, 1, 0},
			{0, 0, 0},
		},
		// "/"
		fontData{
			{0, 0, 1},
			{0, 1, 0},
			{0, 1, 0},
			{0, 1, 0},
			{1, 0, 0},
		},
		// " "
		fontData{
			{0, 0, 0},
			{0, 0, 0},
			{0, 0, 0},
			{0, 0, 0},
			{0, 0, 0},
		},
	}

	//memo : 0->黒, 1->白, 2->青, 3->赤, 4->黄
	icons = [9]iconData{
		// "01 clear sky"
		iconData{
			{0, 0, 0, 0, 0, 0, 0},
			{0, 3, 0, 3, 0, 3, 0},
			{0, 0, 3, 3, 3, 0, 0},
			{0, 3, 3, 3, 3, 3, 0},
			{0, 0, 3, 3, 3, 0, 0},
			{0, 3, 0, 3, 0, 3, 0},
			{0, 0, 0, 0, 0, 0, 0},
		},
		// "02 few clouds"
		iconData{
			{0, 0, 0, 0, 0, 0, 0},
			{0, 0, 0, 3, 0, 0, 3},
			{0, 1, 1, 0, 3, 3, 0},
			{1, 1, 1, 1, 3, 3, 0},
			{1, 1, 1, 1, 1, 1, 3},
			{1, 1, 1, 1, 1, 1, 0},
			{0, 0, 0, 0, 0, 0, 0},
		},
		// "03 scatterd clouds & 04 broken clouds"
		iconData{
			{0, 0, 0, 0, 0, 0, 0},
			{0, 0, 1, 1, 0, 0, 0},
			{0, 1, 1, 1, 1, 0, 0},
			{0, 1, 1, 1, 1, 1, 0},
			{1, 1, 1, 1, 1, 1, 1},
			{1, 1, 1, 1, 1, 1, 1},
			{0, 0, 0, 0, 0, 0, 0},
		},
		// "09 shower rain(短時間の激しい雨らしい)"
		iconData{
			{0, 0, 1, 1, 0, 0, 0},
			{0, 1, 1, 1, 1, 0, 0},
			{1, 1, 1, 1, 1, 1, 1},
			{1, 1, 1, 1, 1, 1, 1},
			{0, 2, 0, 0, 2, 0, 0},
			{0, 0, 2, 0, 0, 2, 0},
			{0, 0, 0, 2, 0, 0, 2},
		},
		// "10 rain"
		iconData{
			{0, 0, 1, 1, 0, 0, 0},
			{0, 1, 1, 1, 1, 0, 0},
			{1, 1, 1, 1, 1, 1, 1},
			{1, 1, 1, 1, 1, 1, 1},
			{0, 2, 0, 0, 2, 0, 0},
			{0, 0, 0, 0, 0, 0, 0},
			{0, 0, 2, 0, 0, 2, 0},
		},
		// "11 thunderstorm"
		iconData{
			{0, 0, 1, 1, 0, 0, 0},
			{0, 1, 1, 1, 1, 0, 0},
			{1, 1, 1, 1, 1, 1, 1},
			{1, 1, 1, 1, 1, 1, 1},
			{0, 4, 0, 0, 4, 0, 0},
			{4, 0, 0, 4, 0, 0, 0},
			{0, 4, 0, 0, 4, 0, 0},
		},
		// "13 snow"
		iconData{
			{0, 0, 1, 1, 0, 0, 0},
			{0, 1, 1, 1, 1, 0, 0},
			{1, 1, 1, 1, 1, 1, 1},
			{1, 1, 1, 1, 1, 1, 1},
			{0, 1, 0, 0, 1, 0, 0},
			{0, 0, 0, 0, 0, 0, 0},
			{0, 0, 1, 0, 0, 1, 0},
		},
		// "50 mist"
		iconData{
			{1, 1, 1, 1, 1, 1, 0},
			{0, 0, 0, 0, 0, 0, 0},
			{0, 0, 1, 1, 1, 1, 1},
			{0, 0, 0, 0, 0, 0, 0},
			{1, 1, 1, 1, 1, 0, 0},
			{0, 0, 0, 0, 0, 0, 0},
			{0, 1, 1, 1, 1, 1, 1},
		},
		// "? unknown"
		iconData{
			{0, 0, 1, 1, 1, 0, 0},
			{0, 1, 0, 0, 0, 1, 0},
			{0, 1, 0, 0, 0, 1, 0},
			{0, 0, 0, 0, 1, 0, 0},
			{0, 0, 0, 1, 0, 0, 0},
			{0, 0, 0, 0, 0, 0, 0},
			{0, 0, 0, 1, 0, 0, 0},
		},
	}
)

func main() {
	// waitSerial()

	netLinker, _ := probe.Probe()

	err := netLinker.NetConnect(&netlink.ConnectParams{
		Ssid:       ssid,
		Passphrase: password,
	})
	if err != nil {
		fmt.Println(err)
	}

	apiURL = fmt.Sprintf("http://api.openweathermap.org/data/3.0/onecall?lat=34.69&lon=135.50&units=metric&exclude=current,minutely,daily,alerts&appid=%s", url.QueryEscape(openWeatherAPIkey))

	spi := machine.SPI0
	spi.Configure(machine.SPIConfig{
		SCK: machine.SPI0_SCK_PIN,
		SDO: machine.SPI0_SDO_PIN,
		SDI: machine.SPI0_SDI_PIN,
	})
	time.Sleep(500 * time.Millisecond)

	latPin := machine.D1
	oePin := machine.D2
	aPin := machine.D3
	bPin := machine.D4
	cPin := machine.D5
	dPin := machine.D6
	h75 := New(spi, latPin, oePin, aPin, bPin, cPin, dPin)
	h75_config := Config{
		Width:  64,
		Height: 32,
	}
	h75.Configure(h75_config)
	h75.SetBrightness(255)

	h75.ClearDisplay()
	h75.FlushDisplay()

	ch := make(chan WeatherInfoList, 1)
	go fetchData(ch)

	for {
		select {
		case wi := <-ch:
			// debug用
			for _, data := range wi.Info {
				fmt.Printf("date : %s, hour : %s, weatherID : %s\n", wi.Date, data.Hour, data.ID)
			}
			fmt.Println("------------------------------------------")

			h75.ClearDisplay()
			h75.FlushDisplay()

			for i, data := range wi.Info {
				putString(h75, (i*10)+2, 1, color.RGBA{0xFF, 0xFF, 0x00, 0xFF}, data.Hour)
				putIcon(h75, (i*10)+2, 7, data.ID)
			}
			putString(h75, 24, 32-6, color.RGBA{0xFF, 0xFF, 0xFF, 0xFF}, wi.Date)
		default:
		}
		err := h75.Display()
		if err != nil {
			log.Fatal(err)
		}
	}
}

func waitSerial() {
	for !machine.Serial.DTR() {
		time.Sleep(100 * time.Millisecond)
	}
}

// 20分ごとに6時間分の天気情報を取得する
func fetchData(sendCh chan<- WeatherInfoList) {
	for {
		data := &WeatherJSON{}
		response, err := http.Get(apiURL)
		if err != nil {
			fmt.Println(err)
			time.Sleep(5 * time.Second)
		}

		sizeCopied := 0
		for {
			n, err := response.Body.Read(readBuf[:])
			if err != nil && err != io.EOF {
				fmt.Println(err)
				time.Sleep(5 * time.Second)
			}
			if n == 0 {
				break
			}

			if sizeCopied+n > len(httpBody) {
				log.Fatal(fmt.Errorf("http response is bigger than buffer(httpBody)"))
				break
			}

			copy(httpBody[sizeCopied:], readBuf[:n])
			sizeCopied += n
		}
		response.Body.Close()

		err = json.Unmarshal(httpBody[:sizeCopied], data)
		if err != nil {
			fmt.Println(err)
		}
		weatherInfoList := WeatherInfoList{}
		for i, dataPerHour := range data.Hourly {
			// タイムゾーンのオフセットを加算した時間を算出する
			t := time.Unix(int64(dataPerHour.Dt+data.TimezoneOffset), 0)

			// 日付情報を取得する
			if i == 0 {
				weatherInfoList.Date = fmt.Sprintf("%4d/%2d/%2d", t.Year(), t.Month(), t.Day())
			}

			h := fmt.Sprintf("%02d", t.Hour())
			d := dataPerHour.Weather[0].Icon[0:2]
			weatherInfoList.Info = append(weatherInfoList.Info, WeatherInfo{Hour: h, ID: d})
		}

		sendCh <- weatherInfoList

		time.Sleep(20 * time.Minute)
	}
}

func putFont(h75 Device, vx, vy int, c color.RGBA, font [5][3]uint8) {
	for y, lineData := range font {
		for x, v := range lineData {
			if v == 1 {
				h75.SetPixel(int16(x+vx), int16(y+vy), c)
			}
		}
	}
}

func putString(h75 Device, vx, vy int, c color.RGBA, str string) {
	strSlice := strings.Split(str, "")
	for _, s := range strSlice {
		if s == ":" {
			putFont(h75, vx, vy, c, fonts[10])
		} else if s == "/" {
			putFont(h75, vx, vy, c, fonts[11])
		} else if s == " " {
			putFont(h75, vx, vy, c, fonts[12])
		} else {
			v, err := strconv.Atoi(s)
			if err != nil {
				log.Fatal(fmt.Errorf("Available strings are [0-9:/]"))
			}
			putFont(h75, vx, vy, c, fonts[v])
		}
		vx += 4
	}
}

// API が返す icon の 数値を利用し 自作した icon を選択する
// https://openweathermap.org/weather-conditions#How-to-get-icon-URL
func putIcon(h75 Device, vx, vy int, weatherID string) {
	index := -1
	switch weatherID[:2] {
	case "01":
		index = 0
	case "02":
		index = 1
	case "03":
		index = 2
	case "04":
		// 03 と同じ
		index = 2
	case "09":
		index = 3
	case "10":
		index = 4
	case "11":
		index = 5
	case "13":
		index = 6
	case "50":
		index = 7
	default:
		index = 8
		// debug用
		// fmt.Println("weatherID is not incorrect. index:%d", index)
	}

	for y, lineData := range icons[index] {
		for x, v := range lineData {
			c := color.RGBA{0x00, 0x00, 0x00, 0x00}
			switch v {
			case 1:
				c = color.RGBA{0xFF, 0xFF, 0xFF, 0xFF}
			case 2:
				c = color.RGBA{0x00, 0x00, 0xFF, 0xFF}
			case 3:
				c = color.RGBA{0xFF, 0x00, 0x00, 0xFF}
			case 4:
				c = color.RGBA{0xFF, 0xFF, 0x00, 0xFF}
			}
			h75.SetPixel(int16(x+vx), int16(y+vy), c)
		}
	}
}
