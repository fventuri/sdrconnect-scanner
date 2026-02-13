// scanner using SDRconnect
//
// Copyright 2026 Franco Venturi.
//
// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"encoding/csv"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/eiannone/keyboard"
	"golang.org/x/net/websocket"
	"gopkg.in/ini.v1"
)

type Message struct {
	EventType string `json:"event_type"`
	Property  string `json:"property"`
	Value     string `json:"value"`
}

type DemodulatorMode int

const (
	DemodulatorUnknown DemodulatorMode = iota
	DemodulatorAM
	DemodulatorUSB
	DemodulatorLSB
	DemodulatorCW
	DemodulatorSAM
	DemodulatorNFM
	DemodulatorWFM
)

func (dm DemodulatorMode) String() string {
	switch dm {
	case DemodulatorUnknown:
		return "UNKNOWN"
	case DemodulatorAM:
		return "AM"
	case DemodulatorUSB:
		return "USB"
	case DemodulatorLSB:
		return "LSB"
	case DemodulatorCW:
		return "CW"
	case DemodulatorSAM:
		return "SAM"
	case DemodulatorNFM:
		return "NFM"
	case DemodulatorWFM:
		return "WFM"
	default:
		return fmt.Sprintf("invalid demodulator mode: %d", dm)
	}
}

func ParseDemodulatorMode(dmstring string) (DemodulatorMode, error) {
	switch dmstring {
	case "AM":
		return DemodulatorAM, nil
	case "USB":
		return DemodulatorUSB, nil
	case "LSB":
		return DemodulatorLSB, nil
	case "CW":
		return DemodulatorCW, nil
	case "SAM":
		return DemodulatorSAM, nil
	case "NFM":
		return DemodulatorNFM, nil
	case "WFM":
		return DemodulatorWFM, nil
	default:
		return DemodulatorUnknown, fmt.Errorf("invalid demodulator mode: %s", dmstring)
	}
}

type LOSpan struct {
	from      int
	to        int
	frequency uint64
}

type Scan struct {
	Start                uint64
	Stop                 uint64
	Step                 int64
	List                 []uint64
	DeviceName           string
	DeviceSerial         string
	Profile              string
	DetectPowerThreshold float64
	DetectSNRThreshold   float64
	DetectTime           time.Duration
	ListenTime           time.Duration
	ListenExtraTimeRDS   time.Duration
	LOOffset             int32
	LOSpans              []LOSpan
	// SDRconnect properties
	SampleRate       float64
	Demodulator      DemodulatorMode
	LNAStateSet      bool
	LNAState         uint32
	SquelchEnable    bool
	SquelchThreshold float64
	AGCEnable        bool
	AGCThreshold     float64
}

type SDRconnectSettings struct {
	DeviceName            string
	DeviceSerial          string
	Profile               string
	SampleRate            float64
	FilterBandwidth       uint32
	DeviceCenterFrequency uint64
	DeviceVFOFrequency    uint64
	// SDRconnect properties
	Demodulator      DemodulatorMode
	LNAState         uint32
	SquelchEnable    bool
	SquelchThreshold float64
	AGCEnable        bool
	AGCThreshold     float64
}

type FrequencyAndIndex struct {
	frequency uint64
	index     int
}

type FrequencyAndLOFrequency struct {
	frequency   uint64
	loFrequency uint64
}

type ReceiveStats struct {
	countMessages int
	signalPower   []float64
	signalSNR     []float64
	rdsPI         []uint16
	rdsPS         []string
}

// global variables
var ws *websocket.Conn
var defaultSection *ini.Section
var labels = make(map[uint64]string)
var sdrconnectSettings = SDRconnectSettings{}
var maxStats = 100
var receiveStats = ReceiveStats{
	signalPower: make([]float64, 0, maxStats),
	signalSNR:   make([]float64, 0, maxStats),
	rdsPI:       make([]uint16, 0, maxStats),
	rdsPS:       make([]string, 0, maxStats),
}

var debug bool

// wait times
var waitGetProperty = 1000 * time.Millisecond
var waitSetProperty = 2000 * time.Millisecond
var waitSelectDevice = 6000 * time.Millisecond
var waitApplyProfile = 600 * time.Millisecond
var waitSetCenterFrequency = 1000 * time.Millisecond
var waitSignalPowerAndSNR = 600 * time.Millisecond

var defaultDetectTime = waitSignalPowerAndSNR
var defaultListenTime = 5 * time.Second

// user commands
var userCommandTogglePause bool
var userCommandNextScan bool
var userCommandTerminate bool

// custom errors to pass user commands
var ErrUserCommandNextScan = errors.New("user command nextscan")
var ErrUserCommandTerminate = errors.New("user command terminate")

func main() {
	var wsAddress string
	flag.StringVar(&wsAddress, "ws", "127.0.0.1:5454", "SDRconnect web socket address (IP:port)")
	var configFile string
	flag.StringVar(&configFile, "conf", "", "scanner configuration file")
	var labelFile string
	flag.StringVar(&labelFile, "labels", "", "CSV file with labels")
	flag.BoolVar(&debug, "debug", false, "enable debug")
	flag.Parse()

	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	if configFile == "" {
		log.Fatal("missing configuration file")
	}

	scans, err := readConfigFile(configFile)
	if err != nil {
		log.Fatal("error reading configuration file: ", err)
	}

	if labelFile != "" {
		err = readLabelFile(labelFile)
		if err != nil {
			log.Fatal("error reading label file: ", err)
		}
	}

	wsIp := strings.Split(wsAddress, ":")[0]
	origin := fmt.Sprintf("http://%s/", wsIp)
	url := fmt.Sprintf("ws://%s/", wsAddress)
	ws, err = websocket.Dial(url, "", origin)
	if err != nil {
		log.Fatal(err)
	}
	defer ws.Close()

	if err = keyboard.Open(); err != nil {
		log.Fatal(err)
	}
	defer keyboard.Close()
	go getKeyPresses()

	sdrconnectSettings, err = getSdrconnectSettings()
	if err != nil {
		log.Fatal(err)
	}

	// main scan loop
	for {
		for idx := range scans {
			scan := &scans[idx]
			err = initScan(scan)
			if err != nil {
				if errors.Is(err, ErrUserCommandTerminate) {
					err = nil
					return
				} else if errors.Is(err, ErrUserCommandNextScan) {
					err = nil
					continue
				} else {
					log.Println("init scan error:", err)
					return
				}
			}
			err = runScan(scan)
			if err != nil {
				if errors.Is(err, ErrUserCommandTerminate) {
					err = nil
					return
				} else if errors.Is(err, ErrUserCommandNextScan) {
					err = nil
					continue
				} else {
					log.Println("scan error:", err)
					return
				}
			}
		}
	}
}

func readConfigFile(configFile string) (scans []Scan, err error) {
	config, err := ini.LoadSources(
		ini.LoadOptions{
			AllowNonUniqueSections: true,
		},
		configFile,
	)
	if err != nil {
		return nil, err
	}
	config.BlockMode = false
	defaultSection, err = config.GetSection("")
	if err != nil {
		return nil, err
	}

	scanSections, err := config.SectionsByName("scan")
	if err != nil {
		return nil, err
	}

	for _, section := range scanSections {

		hasRange := section.HasKey("range")
		hasList := section.HasKey("list")
		if (!hasRange && !hasList) || (hasRange && hasList) {
			err := fmt.Errorf("scan section should have a 'range' or 'list' setting (but not both)")
			return nil, err
		}

		var freqStart uint64
		var freqStop uint64
		var freqStep int64
		var freqList []uint64
		if hasRange {
			freqRangeValues := section.Key("range").Float64s(",")
			if len(freqRangeValues) != 3 {
				err := fmt.Errorf("range setting must have exactly three values")
				return nil, err
			}
			if freqRangeValues[2] == 0 {
				err := fmt.Errorf("range setting step value must not be 0")
				return nil, err
			}
			if freqRangeValues[2] > 0 && !(freqRangeValues[1] > freqRangeValues[0]) {
				err := fmt.Errorf("range setting end value must be greater than start value")
				return nil, err
			}
			if freqRangeValues[2] < 0 && !(freqRangeValues[1] < freqRangeValues[0]) {
				err := fmt.Errorf("range setting end value must be less than start value (negative step)")
				return nil, err
			}
			freqStart = uint64(freqRangeValues[0])
			freqStop = uint64(freqRangeValues[1])
			freqStep = int64(freqRangeValues[2])
		} else if hasList {
			freqListValues := section.Key("list").Float64s(",")
			if len(freqListValues) == 0 {
				err := fmt.Errorf("invalid frequency scan list")
				return nil, err
			}
			freqList = make([]uint64, 0, len(freqListValues))
			for _, f := range freqListValues {
				freqList = append(freqList, uint64(f))
			}
		}

		deviceName, ok, err := getStringConfigSetting("device name", section)
		if err != nil {
			return nil, err
		}
		deviceSerial, ok, err := getStringConfigSetting("device serial", section)
		if err != nil {
			return nil, err
		}
		if deviceName != "" && deviceSerial != "" {
			err = fmt.Errorf("select only one of 'device name' or 'device serial'")
			return nil, err
		}
		profile, ok, err := getStringConfigSetting("profile", section)
		if err != nil {
			return nil, err
		}

		detectPowerThreshold, ok, err := getFloat64ConfigSetting("detect power threshold", section)
		if err != nil {
			return nil, err
		}
		detectSNRThreshold, ok, err := getFloat64ConfigSetting("detect snr threshold", section)
		if err != nil {
			return nil, err
		}
		detectTimeMs, ok, err := getUint32ConfigSetting("detect time", section)
		if err != nil {
			return nil, err
		}
		detectTime := defaultDetectTime
		if ok {
			detectTime = time.Duration(detectTimeMs) * time.Millisecond
			if detectTime < waitSignalPowerAndSNR {
				err = fmt.Errorf("detect time should be at least %v", waitSignalPowerAndSNR)
				return nil, err
			}
		}
		listenTimeMs, ok, err := getUint32ConfigSetting("listen time", section)
		if err != nil {
			return nil, err
		}
		listenTime := defaultListenTime
		if ok {
			listenTime = time.Duration(listenTimeMs) * time.Millisecond
		}
		listenTimeRDSMs, ok, err := getUint32ConfigSetting("listen time rds", section)
		if err != nil {
			return nil, err
		}
		var listenExtraTimeRDS time.Duration
		if ok {
			listenTimeRDS := time.Duration(listenTimeRDSMs) * time.Millisecond
			if listenTimeRDS < listenTime {
				err = fmt.Errorf("listen time rds should be greater than or equal to listen time")
				return nil, err
			}
			listenExtraTimeRDS = listenTimeRDS - listenTime
		}
		loOffsetFloat, ok, err := getFloat64ConfigSetting("lo offset", section)
		if err != nil {
			return nil, err
		}
		loOffset := int32(loOffsetFloat)

		// SDRconnect properties
		sampleRate, ok, err := getFloat64ConfigSetting("sample rate", section)
		if err != nil {
			return nil, err
		}
		demodulatorString, ok, err := getStringConfigSetting("demodulator", section)
		if err != nil {
			return nil, err
		}
		var demodulator DemodulatorMode
		if ok {
			demodulator, err = ParseDemodulatorMode(demodulatorString)
			if err != nil {
				return nil, err
			}
		}
		lnaState, ok, err := getUint32ConfigSetting("lna state", section)
		if err != nil {
			return nil, err
		}
		lnaStateSet := ok
		squelchThreshold, ok, err := getFloat64ConfigSetting("squelch", section)
		if err != nil {
			return nil, err
		}
		squelchEnable := ok
		agcThreshold, ok, err := getFloat64ConfigSetting("agc", section)
		if err != nil {
			return nil, err
		}
		agcEnable := ok

		scans = append(scans, Scan{
			Start:                freqStart,
			Stop:                 freqStop,
			Step:                 freqStep,
			List:                 freqList,
			DeviceName:           deviceName,
			DeviceSerial:         deviceSerial,
			Profile:              profile,
			DetectPowerThreshold: detectPowerThreshold,
			DetectSNRThreshold:   detectSNRThreshold,
			DetectTime:           detectTime,
			ListenTime:           listenTime,
			ListenExtraTimeRDS:   listenExtraTimeRDS,
			LOOffset:             loOffset,
			SampleRate:           sampleRate,
			Demodulator:          demodulator,
			LNAStateSet:          lnaStateSet,
			LNAState:             lnaState,
			SquelchEnable:        squelchEnable,
			SquelchThreshold:     squelchThreshold,
			AGCEnable:            agcEnable,
			AGCThreshold:         agcThreshold,
		})
	}
	return
}

func readLabelFile(labelFile string) (err error) {
	var file *os.File
	file, err = os.Open(labelFile)
	if err != nil {
		return
	}
	defer file.Close()

	reader := csv.NewReader(file)
	reader.Comment = '#'

	for {
		var record []string
		record, err = reader.Read()
		if err == io.EOF {
			err = nil
			break
		}
		if err != nil {
			return
		}
		if len(record) != 2 {
			err = fmt.Errorf("invalid label record: %v", record)
			return
		}
		var key uint64
		if len(record[0]) == 4 {
			// PI code
			key, err = strconv.ParseUint(record[0], 16, 16)
		} else {
			// frequency
			key, err = strconv.ParseUint(record[0], 10, 64)
		}
		if err != nil {
			return
		}
		label := strings.TrimSpace(record[1])
		currentLabel, ok := labels[key]
		if ok {
			labels[key] = currentLabel + "|" + label
		} else {
			labels[key] = label
		}
	}
	return
}

func getSdrconnectSettings() (settings SDRconnectSettings, err error) {
	var result string
	result, err = getSdrconnectProperty("device_sample_rate")
	if err != nil {
		return
	}
	settings.SampleRate, err = strconv.ParseFloat(result, 64)
	if err != nil {
		return
	}

	result, err = getSdrconnectProperty("filter_bandwidth")
	if err != nil {
		return
	}
	var filterBandwidth uint64
	filterBandwidth, err = strconv.ParseUint(result, 10, 32)
	if err != nil {
		return
	}
	settings.FilterBandwidth = uint32(filterBandwidth)

	// SDRconnect properties
	result, err = getSdrconnectProperty("demodulator")
	if err != nil {
		return
	}
	settings.Demodulator, err = ParseDemodulatorMode(result)
	if err != nil {
		return
	}
	result, err = getSdrconnectProperty("lna_state")
	if err != nil {
		return
	}
	var lnaState uint64
	lnaState, err = strconv.ParseUint(result, 10, 32)
	if err != nil {
		return
	}
	settings.LNAState = uint32(lnaState)
	result, err = getSdrconnectProperty("squelch_enable")
	if err != nil {
		return
	}
	settings.SquelchEnable, err = strconv.ParseBool(result)
	if err != nil {
		return
	}
	result, err = getSdrconnectProperty("squelch_threshold")
	if err != nil {
		return
	}
	settings.SquelchThreshold, err = strconv.ParseFloat(result, 64)
	if err != nil {
		return
	}
	result, err = getSdrconnectProperty("agc_enable")
	if err != nil {
		return
	}
	settings.AGCEnable, err = strconv.ParseBool(result)
	if err != nil {
		return
	}
	result, err = getSdrconnectProperty("agc_threshold")
	if err != nil {
		return
	}
	settings.AGCThreshold, err = strconv.ParseFloat(result, 64)
	if err != nil {
		return
	}

	return
}

func getKeyPresses() {
	for {
		char, key, err := keyboard.GetKey()
		if err != nil {
			log.Fatal(err)
		}
		if key == keyboard.KeyCtrlC || char == 'q' || char == 'Q' {
			userCommandTerminate = true
			break
		} else if key == keyboard.KeySpace {
			userCommandTogglePause = true
		} else if char == 'n' || char == 'N' {
			userCommandNextScan = true
		}
	}
}

func initScan(scan *Scan) (err error) {
	if scan.DeviceName != "" {
		if scan.DeviceName != sdrconnectSettings.DeviceName {
			err = selectSdrconnectDeviceByName(scan.DeviceName)
			if err != nil {
				return
			}
			sdrconnectSettings.DeviceName = scan.DeviceName
		}
	} else if scan.DeviceSerial != "" {
		if scan.DeviceSerial != sdrconnectSettings.DeviceSerial {
			err = selectSdrconnectDeviceBySerial(scan.DeviceSerial)
			if err != nil {
				return
			}
			sdrconnectSettings.DeviceSerial = scan.DeviceSerial
		}
	}
	if scan.Profile != sdrconnectSettings.Profile {
		err = applySdrconnectProfile(scan.Profile)
		if err != nil {
			return
		}
		sdrconnectSettings.Profile = scan.Profile
	}

	// set specific SDRconnect properties are requested
	var result string

	if scan.SampleRate != 0 {
		if scan.SampleRate != sdrconnectSettings.SampleRate {
			sampleRate := strconv.FormatFloat(scan.SampleRate, 'f', -1, 64)
			var actualSampleRate string
			actualSampleRate, _, err = setSdrconnectProperty("device_sample_rate", sampleRate)
			if err != nil {
				return
			}
			sdrconnectSettings.SampleRate, err = strconv.ParseFloat(actualSampleRate, 64)
			if err != nil {
				return
			}
		}
	}

	if scan.Demodulator != DemodulatorUnknown {
		if scan.Demodulator != sdrconnectSettings.Demodulator {
			var demodulator string
			demodulator, _, err = setSdrconnectProperty("demodulator", scan.Demodulator.String())
			if err != nil {
				return
			}
			sdrconnectSettings.Demodulator, err = ParseDemodulatorMode(demodulator)
			if err != nil {
				return
			}
		}
	}

	if scan.LNAStateSet {
		if scan.LNAState != sdrconnectSettings.LNAState {
			lnaState := strconv.FormatUint(uint64(scan.LNAState), 10)
			_, _, err = setSdrconnectProperty("lna_state", lnaState)
			if err != nil {
				return
			}
			sdrconnectSettings.LNAState = scan.LNAState
		}
	}

	if scan.SquelchEnable {
		if scan.SquelchEnable != sdrconnectSettings.SquelchEnable {
			_, _, err = setSdrconnectProperty("squelch_enable", "true")
			if err != nil {
				return
			}
			sdrconnectSettings.SquelchEnable = scan.SquelchEnable
		}
		if scan.SquelchThreshold != sdrconnectSettings.SquelchThreshold {
			squelchThreshold := strconv.FormatFloat(scan.SquelchThreshold, 'f', -1, 64)
			_, _, err = setSdrconnectProperty("squelch_threshold", squelchThreshold)
			if err != nil {
				return
			}
			sdrconnectSettings.SquelchThreshold = scan.SquelchThreshold
		}
	}

	if scan.AGCEnable {
		if scan.AGCEnable != sdrconnectSettings.AGCEnable {
			_, _, err = setSdrconnectProperty("agc_enable", "true")
			if err != nil {
				return
			}
			sdrconnectSettings.AGCEnable = scan.AGCEnable
		}
		if scan.AGCThreshold != sdrconnectSettings.AGCThreshold {
			agcThreshold := strconv.FormatFloat(scan.AGCThreshold, 'f', -1, 64)
			_, _, err = setSdrconnectProperty("agc_threshold", agcThreshold)
			if err != nil {
				return
			}
			sdrconnectSettings.AGCThreshold = scan.AGCThreshold
		}
	}

	// make sure we know the current sample rate and filter bandwidth
	if sdrconnectSettings.SampleRate == 0 {
		result, err = getSdrconnectProperty("device_sample_rate")
		if err != nil {
			return
		}
		sdrconnectSettings.SampleRate, err = strconv.ParseFloat(result, 64)
		if err != nil {
			return
		}
	}

	if sdrconnectSettings.FilterBandwidth == 0 {
		result, err = getSdrconnectProperty("filter_bandwidth")
		if err != nil {
			return
		}
		var filterBandwidth uint64
		filterBandwidth, err = strconv.ParseUint(result, 10, 32)
		if err != nil {
			return
		}
		sdrconnectSettings.FilterBandwidth = uint32(filterBandwidth)
	}

	if scan.LOSpans == nil {
		scan.LOSpans = getLOSpans(scan)
	}

	return
}

func runScan(scan *Scan) (err error) {
	for freqAndLOFreq := range getScanFrequenciesAndLOFrequencies(scan) {
		loFreq := freqAndLOFreq.loFrequency
		if loFreq != 0 && loFreq != sdrconnectSettings.DeviceCenterFrequency {
			err = setCenterFrequency(loFreq)
			if err != nil {
				return
			}
		}

		// clear receive stats
		receiveStats.countMessages = 0
		receiveStats.signalPower = receiveStats.signalPower[:0]
		receiveStats.signalSNR = receiveStats.signalSNR[:0]
		receiveStats.rdsPI = receiveStats.rdsPI[:0]
		receiveStats.rdsPS = receiveStats.rdsPS[:0]

		freq := freqAndLOFreq.frequency
		err = setVFOFrequencyAndGetSignalStats(freq, scan.DetectTime)
		if err != nil {
			return
		}
		if detectSignal(scan) {
			showStats("detect")
			err = receiveMessages(&sdrconnectSettings, nil, scan.ListenTime)
			if err != nil {
				return
			}
			if len(receiveStats.rdsPI) > 0 && scan.ListenExtraTimeRDS > 0 {
				err = receiveMessages(&sdrconnectSettings, nil, scan.ListenExtraTimeRDS)
				if err != nil {
					return
				}
			}
			showStats("listen")
		}
	}
	return
}

// auxiliary functions

// config file
func getStringConfigSetting(setting string, section *ini.Section) (value string, ok bool, err error) {
	if section.HasKey(setting) {
		value = section.Key(setting).String()
		ok = true
	} else if defaultSection.HasKey(setting) {
		value = defaultSection.Key(setting).String()
		ok = true
	}
	return
}

func getUint32ConfigSetting(setting string, section *ini.Section) (value uint32, ok bool, err error) {
	var valueUint uint
	if section.HasKey(setting) {
		valueUint, err = section.Key(setting).Uint()
		ok = true
	} else if defaultSection.HasKey(setting) {
		valueUint, err = defaultSection.Key(setting).Uint()
		ok = true
	}
	if err == nil {
		value = uint32(valueUint)
	}
	return
}

func getUint64ConfigSetting(setting string, section *ini.Section) (value uint64, ok bool, err error) {
	if section.HasKey(setting) {
		value, err = section.Key(setting).Uint64()
		ok = true
	} else if defaultSection.HasKey(setting) {
		value, err = defaultSection.Key(setting).Uint64()
		ok = true
	}
	return
}

func getFloat64ConfigSetting(setting string, section *ini.Section) (value float64, ok bool, err error) {
	if section.HasKey(setting) {
		value, err = section.Key(setting).Float64()
		ok = true
	} else if defaultSection.HasKey(setting) {
		value, err = defaultSection.Key(setting).Float64()
		ok = true
	}
	return
}

// SDRconnect via websocket interface
func getSdrconnectProperty(property string) (value string, err error) {
	request := Message{
		EventType: "get_property",
		Property:  property,
	}
	err = websocket.JSON.Send(ws, request)
	if err != nil {
		return
	}
	var message Message
	ws.SetReadDeadline(time.Now().Add(waitGetProperty))
	defer ws.SetReadDeadline(time.Time{})
	for {
		err = websocket.JSON.Receive(ws, &message)
		if err != nil {
			err = fmt.Errorf("getSdrconnectProperty(%s): %w", property, err)
			return
		}
		if message.EventType == "get_property_response" {
			if message.Property == property {
				value = message.Value
				return
			}
		}
	}
}

func setSdrconnectProperty(property string, value string) (actualValue string, changed bool, err error) {
	request := Message{
		EventType: "set_property",
		Property:  property,
		Value:     value,
	}
	err = websocket.JSON.Send(ws, request)
	if err != nil {
		return
	}
	var message Message
	ws.SetReadDeadline(time.Now().Add(waitSetProperty))
	defer ws.SetReadDeadline(time.Time{})
	for {
		err = websocket.JSON.Receive(ws, &message)
		if err != nil {
			// ignore timeouts because the property might already
			// have been at the correct value
			if errors.Is(err, os.ErrDeadlineExceeded) {
				actualValue = value
				err = nil
			} else {
				err = fmt.Errorf("setSdrconnectProperty(%s): %w", property, err)
			}
			return
		}
		if debug {
			log.Println("message:", message.EventType, message.Property, message.Value)
		}
		if message.EventType == "property_changed" {
			if message.Property == property {
				actualValue = message.Value
				changed = true
				return
			}
		}
	}
}

func selectSdrconnectDeviceByName(device_name string) (err error) {
	request := Message{
		EventType: "selected_device_name",
		Value:     device_name,
	}
	err = websocket.JSON.Send(ws, request)
	if err != nil {
		return
	}
	err = receiveMessages(&sdrconnectSettings, nil, waitSelectDevice)
	return
}

func selectSdrconnectDeviceBySerial(device_serial string) (err error) {
	request := Message{
		EventType: "selected_device_serial",
		Value:     device_serial,
	}
	err = websocket.JSON.Send(ws, request)
	if err != nil {
		return
	}
	err = receiveMessages(&sdrconnectSettings, nil, waitSelectDevice)
	return
}

func applySdrconnectProfile(profile string) (err error) {
	request := Message{
		EventType: "apply_device_profile",
		Value:     profile,
	}
	err = websocket.JSON.Send(ws, request)
	if err != nil {
		return
	}
	// fv
	//err = receiveMessages(&sdrconnectSettings, regexp.MustCompile("^.*VCVC$"), waitApplyProfile)
	err = receiveMessages(&sdrconnectSettings, nil, waitApplyProfile)
	return
}

func receiveMessages(settings *SDRconnectSettings, sequencePattern *regexp.Regexp, timeout time.Duration) (err error) {
	var message Message
	var sequence string
	var paused bool
	ws.SetReadDeadline(time.Now().Add(timeout))
	defer ws.SetReadDeadline(time.Time{})
	for {
		err = websocket.JSON.Receive(ws, &message)
		if err != nil {
			if errors.Is(err, os.ErrDeadlineExceeded) && sequencePattern == nil && receiveStats.countMessages > 0 {
				err = nil
			}
			return
		}
		receiveStats.countMessages++
		if debug {
			log.Println("message:", message.EventType, message.Property, message.Value)
		}

		// handle user commands
		if userCommandTerminate {
			userCommandTerminate = false
			err = ErrUserCommandTerminate
			return
		} else if userCommandNextScan {
			userCommandNextScan = false
			err = ErrUserCommandNextScan
			return
		} else if userCommandTogglePause {
			userCommandTogglePause = false
			if !paused {
				ws.SetReadDeadline(time.Time{})
			} else {
				ws.SetReadDeadline(time.Now())
			}
			paused = !paused
		}

		if message.EventType == "property_changed" {
			switch message.Property {
			case "device_sample_rate":
				settings.SampleRate, _ = strconv.ParseFloat(message.Value, 64)
				sequence += "S"
			case "device_vfo_frequency":
				settings.DeviceVFOFrequency, _ = strconv.ParseUint(message.Value, 10, 64)
				sequence += "V"
			case "device_center_frequency":
				settings.DeviceCenterFrequency, _ = strconv.ParseUint(message.Value, 10, 64)
				sequence += "C"
			case "filter_bandwidth":
				filterBandwidth, _ := strconv.ParseUint(message.Value, 10, 32)
				settings.FilterBandwidth = uint32(filterBandwidth)
				sequence += "F"
			case "signal_power":
				if len(receiveStats.signalPower) < cap(receiveStats.signalPower) {
					signalPower, _ := strconv.ParseFloat(message.Value, 64)
					receiveStats.signalPower = append(receiveStats.signalPower, signalPower)
				}
			case "signal_snr":
				if len(receiveStats.signalSNR) < cap(receiveStats.signalSNR) {
					signalSNR, _ := strconv.ParseFloat(message.Value, 64)
					receiveStats.signalSNR = append(receiveStats.signalSNR, signalSNR)
				}
			case "rds_pi":
				if len(receiveStats.rdsPI) < cap(receiveStats.rdsPI) {
					rdsPI, _ := strconv.ParseUint(message.Value, 10, 16)
					if rdsPI != 0 {
						receiveStats.rdsPI = append(receiveStats.rdsPI, uint16(rdsPI))
					}
				}
			case "rds_ps":
				if len(receiveStats.rdsPS) < cap(receiveStats.rdsPS) {
					rdsPS := strings.TrimSpace(message.Value)
					if rdsPS != "" {
						receiveStats.rdsPS = append(receiveStats.rdsPS, rdsPS)
					}
				}
			// SDRconnect properties
			case "demodulator":
				settings.Demodulator, _ = ParseDemodulatorMode(message.Value)
			case "lna_state":
				lnaState, _ := strconv.ParseUint(message.Value, 0, 32)
				settings.LNAState = uint32(lnaState)
			case "squelch_enable":
				settings.SquelchEnable, _ = strconv.ParseBool(message.Value)
			case "squelch_threshold":
				settings.SquelchThreshold, _ = strconv.ParseFloat(message.Value, 64)
			case "agc_enable":
				settings.AGCEnable, _ = strconv.ParseBool(message.Value)
			case "agc_threshold":
				settings.AGCThreshold, _ = strconv.ParseFloat(message.Value, 64)
			}
			if sequencePattern != nil && sequencePattern.MatchString(sequence) {
				break
			}
		}
	}
	return
}

func setCenterFrequency(loFreq uint64) (err error) {
	request := Message{
		EventType: "set_property",
		Property:  "device_center_frequency",
		Value:     strconv.FormatUint(loFreq, 10),
	}
	err = websocket.JSON.Send(ws, request)
	if err != nil {
		return
	}
	err = receiveMessages(&sdrconnectSettings, regexp.MustCompile("^.*CV$"), waitSetCenterFrequency)
	if err != nil {
		return
	}
	if sdrconnectSettings.DeviceCenterFrequency != loFreq {
		err = fmt.Errorf("error setting center frequency - requested: %d - actual: %d", loFreq, sdrconnectSettings.DeviceCenterFrequency)
	}
	return
}

func setVFOFrequencyAndGetSignalStats(freq uint64, detectTime time.Duration) (err error) {
	request := Message{
		EventType: "set_property",
		Property:  "device_vfo_frequency",
		Value:     strconv.FormatUint(freq, 10),
	}
	err = websocket.JSON.Send(ws, request)
	if err != nil {
		return
	}
	err = receiveMessages(&sdrconnectSettings, nil, detectTime)
	if err != nil {
		return
	}
	if sdrconnectSettings.DeviceVFOFrequency != freq {
		err = fmt.Errorf("error setting VFO frequency - requested: %d - actual: %d", freq, sdrconnectSettings.DeviceVFOFrequency)
	}
	return
}

// other useful functions
func getIFBandwidth(sampleRate float64) uint32 {
	bandwidthskHz := []uint32{200, 300, 600, 1536, 5000, 6000, 7000, 8000}

	rate := uint32(sampleRate / 1000)
	prevBandwidth := bandwidthskHz[0]
	for _, bandwidth := range bandwidthskHz {
		if rate < bandwidth {
			return prevBandwidth * 1000
		}
		prevBandwidth = bandwidth
	}
	return prevBandwidth * 1000
}

func getLOSpans(scan *Scan) (loSpans []LOSpan) {
	maxDf := uint64(getIFBandwidth(sdrconnectSettings.SampleRate) -
		sdrconnectSettings.FilterBandwidth -
		uint32(max(scan.LOOffset, -scan.LOOffset)))
	var fmin uint64 = math.MaxUint64
	var fmax uint64 = 0
	flo := (fmin + fmax) / 2
	var idxFrom int
	var idxTo int
	for freqAndIdx := range getScanFrequenciesAndIndexes(scan) {
		freq := freqAndIdx.frequency
		idx := freqAndIdx.index
		fmin = min(fmin, freq)
		fmax = max(fmax, freq)
		df := fmax - fmin
		if df > maxDf {
			loSpans = append(loSpans, LOSpan{
				from:      idxFrom,
				to:        idxTo,
				frequency: uint64(int64(flo) + int64(scan.LOOffset)),
			})
			idxFrom = idx
			fmin = freq
			fmax = freq
		}
		flo = (fmin + fmax) / 2
		idxTo = idx
	}
	loSpans = append(loSpans, LOSpan{
		from:      idxFrom,
		to:        idxTo,
		frequency: uint64(int64(flo) + int64(scan.LOOffset)),
	})
	return
}

func detectSignal(scan *Scan) (signalDetected bool) {
	var signalPowerMax float64
	switch len(receiveStats.signalPower) {
	case 0:
		signalPowerMax = -1000
		break
	case 1:
		signalPowerMax = receiveStats.signalPower[0]
	default:
		// ignore the first element since it might be tainted
		// by the previous frequency
		signalPowerMax = slices.Max(receiveStats.signalPower[1:])
	}
	var signalSNRMax float64
	switch len(receiveStats.signalSNR) {
	case 0:
		signalSNRMax = -1000
		break
	case 1:
		signalSNRMax = receiveStats.signalSNR[0]
	default:
		// ignore the first element since it might be tainted
		// by the previous frequency
		signalSNRMax = slices.Max(receiveStats.signalSNR[1:])
	}

	signalDetected = signalPowerMax >= scan.DetectPowerThreshold || signalSNRMax >= scan.DetectSNRThreshold
	return
}

func showStats(what string) {
	var fields []string
	if what != "" {
		fields = append(fields, what)
	}
	freq := sdrconnectSettings.DeviceVFOFrequency
	fields = append(fields, fmt.Sprintf("f=%d", freq))
	if len(receiveStats.rdsPI) > 0 {
		rdsPI := uint64(receiveStats.rdsPI[0])
		if label, ok := labels[rdsPI]; ok {
			fields = append(fields, fmt.Sprintf("l=%s", label))
		}
	}
	if label, ok := labels[freq]; ok {
		fields = append(fields, fmt.Sprintf("l=%s", label))
	}
	if len(receiveStats.signalPower) == 1 {
		fields = append(fields, fmt.Sprintf("pwr=%.1fdB", receiveStats.signalPower[0]))
	} else if len(receiveStats.signalPower) > 1 {
		fields = append(fields, fmt.Sprintf("pwr=[%.1fdB,%.1fdB]", slices.Min(receiveStats.signalPower[1:]), slices.Max(receiveStats.signalPower[1:])))
	}
	if len(receiveStats.signalSNR) == 1 {
		fields = append(fields, fmt.Sprintf("snr=%.1fdB", receiveStats.signalSNR[0]))
	} else if len(receiveStats.signalSNR) > 1 {
		fields = append(fields, fmt.Sprintf("snr=[%.1f.dB,%.1fdB]", slices.Min(receiveStats.signalSNR[1:]), slices.Max(receiveStats.signalSNR[1:])))
	}
	if len(receiveStats.rdsPI) > 0 {
		rdsPIset := make(map[uint16]int)
		for _, rdsPI := range receiveStats.rdsPI {
			rdsPIset[rdsPI]++
		}
		if len(rdsPIset) == 1 {
			rdsPI := receiveStats.rdsPI[0]
			fields = append(fields, fmt.Sprintf("RDS/PI=%04X", rdsPI))
		} else {
			var rdsPIs []string
			for rdsPI, _ := range rdsPIset {
				rdsPIs = append(rdsPIs, fmt.Sprintf("%04X", rdsPI))
			}
			fields = append(fields, fmt.Sprintf("RDS/PI=[%s]", strings.Join(rdsPIs, " ")))
		}
	}
	if len(receiveStats.rdsPS) > 0 {
		fields = append(fields, fmt.Sprintf("RDS/PS=%s", strings.Join(receiveStats.rdsPS, "|")))
	}
	log.Println(strings.Join(fields, " "))
}

// generators
func getScanFrequenciesAndIndexes(scan *Scan) (ch chan FrequencyAndIndex) {
	ch = make(chan FrequencyAndIndex)
	go func() {
		if scan.Step > 0 {
			step := uint64(scan.Step)
			var index int
			for frequency := scan.Start; frequency <= scan.Stop; frequency += step {
				ch <- FrequencyAndIndex{
					frequency: frequency,
					index:     index,
				}
				index++
			}
			close(ch)
		} else if scan.Step < 0 {
			step := uint64(-scan.Step)
			var index int
			for frequency := scan.Start; frequency >= scan.Stop; frequency -= step {
				ch <- FrequencyAndIndex{
					frequency: frequency,
					index:     index,
				}
				index++
			}
			close(ch)
		} else if len(scan.List) > 0 {
			for index, frequency := range scan.List {
				ch <- FrequencyAndIndex{
					frequency: frequency,
					index:     index,
				}
			}
			close(ch)
		} else {
			log.Println("invalid scan: no range and no list")
			close(ch)
		}
	}()
	return ch
}

func getScanFrequenciesAndLOFrequencies(scan *Scan) (ch chan FrequencyAndLOFrequency) {
	ch = make(chan FrequencyAndLOFrequency)
	go func() {
		var loIdx int
		nextLOIdx := scan.LOSpans[loIdx].from
		for freqAndIdx := range getScanFrequenciesAndIndexes(scan) {
			freq := freqAndIdx.frequency
			idx := freqAndIdx.index
			var loFrequency uint64
			if idx == nextLOIdx {
				loFrequency = scan.LOSpans[loIdx].frequency
				loIdx++
				if loIdx < len(scan.LOSpans) {
					nextLOIdx = scan.LOSpans[loIdx].from
				} else {
					nextLOIdx = -1
				}
			}
			ch <- FrequencyAndLOFrequency{
				frequency:   freq,
				loFrequency: loFrequency,
			}
		}
		close(ch)
	}()
	return ch
}
