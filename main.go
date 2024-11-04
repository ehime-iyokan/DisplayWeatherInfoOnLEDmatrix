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

	"tinygo.org/x/drivers/hub75"
	"tinygo.org/x/drivers/netlink"
	"tinygo.org/x/drivers/netlink/probe"
)

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
	h75 := hub75.New(spi, latPin, oePin, aPin, bPin, cPin, dPin)
	h75_config := hub75.Config{
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

func putFont(h75 hub75.Device, vx, vy int, c color.RGBA, font [5][3]uint8) {
	for y, lineData := range font {
		for x, v := range lineData {
			if v == 1 {
				h75.SetPixel(int16(x+vx), int16(y+vy), c)
			}
		}
	}
}

func putString(h75 hub75.Device, vx, vy int, c color.RGBA, str string) {
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
func putIcon(h75 hub75.Device, vx, vy int, weatherID string) {
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
