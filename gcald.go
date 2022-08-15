package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"fyne.io/systray"
	log "github.com/TomLebeda/go_quickLogger"
	ical "github.com/arran4/golang-ical"
	"github.com/gen2brain/beeep"
	"io/ioutil"
	"math"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

type MyCalendar struct {
	Title     string
	Url       string
	Events    map[string]*MyEvent
	ClientCmd string
}

type MyEvent struct {
	Title   string
	Id      string
	Start   time.Time
	End     time.Time
	FullDay bool
	Alarms  []*MyAlarm
}

type MyAlarm struct {
	Event   *MyEvent
	Trigger time.Time
	Used    bool
}

type CalendarMetaData struct {
	Name                 string   `json:"name"`
	Url                  string   `json:"url"`
	Notifications        []string `json:"notification_offsets"`
	FullDayNotifications []string `json:"full_day_notifications_offsets"`
	OpenClientCmd        string   `json:"open_client_cmd"`
	Calendar             *ical.Calendar
}

type Config struct {
	FetchPeriodRaw        string             `json:"fetch_period"`
	ForceDefaultReminders bool               `json:"force_default_reminders"`
	CalendarsMetaData     []CalendarMetaData `json:"calendars"`
	FetchPeriod           time.Duration
	ForceCheckPeriod      time.Duration
}

var (
	cache     = make(map[string]struct{}) // holds the IDs of full-day events to avoid duplicit notifications
	cacheDay  int                         // holds the current day for clearing cache every day
	config    Config
	lastFetch = time.Time{}
	cals      []*MyCalendar
)

var (
	configFileName = "gcald_import.json"
	dataFolderPath = "./data"
)

var interruptCh = make(chan struct{})
var sysReady = make(chan bool)

func main() {
	flag.StringVar(&dataFolderPath, "i", "data/", "path to the folder where gcald_import.json and icons are located. Default is ./data/")
	flag.BoolVar(&log.Tracing, "v", false, "verbose output")
	flag.Parse()
	log.Trace("started")

	importFile(dataFolderPath + configFileName)

	var nearestAlarm *MyAlarm
	var nearestEvent *MyEvent
	forceFetch := false
	fetch()

	go systray.Run(onReady, onExit)

	<-sysReady // before manipulate wich systray wait for initializing

	updateMenuButtons()
	for {
		if time.Now().YearDay() != cacheDay {
			cache = make(map[string]struct{})
			cacheDay = time.Now().YearDay()
		}

		if time.Since(lastFetch) >= config.FetchPeriod || forceFetch {
			fetch()
		}

		nearestAlarm, nearestEvent = check(cals)
		log.Debug("nearest alarm: ", nearestAlarm.Trigger.Format(time.StampMilli))
		log.Debug("sleep time:", time.Until(nearestAlarm.Trigger))
		updateTooltip(nearestEvent)
		select {
		case <-time.After(time.Until(nearestAlarm.Trigger)):
			log.Trace("waking up for nearest alarm")
			notify(nearestAlarm)
			break
		case <-time.After(config.FetchPeriod - time.Since(lastFetch)):
			break
		case <-interruptCh:
			// manual interrupt and request to fetch new data
			forceFetch = true
			break
		}
	}
}

func onReady() {
	var icon []byte
	var err error
	if runtime.GOOS == "windows" {
		icon, err = os.ReadFile(dataFolderPath + "systray_icon.ico")
	} else {
		icon, err = os.ReadFile(dataFolderPath + "systray_icon.png")
	}
	if err != nil {
		log.Fatalf("failed to load systray icon: %s", err.Error())
	} else {
		systray.SetIcon(icon)
		log.Trace("systray icon loaded")
	}
	systray.SetTitle("gcald")
	sysReady <- true
}

func onExit() {
	log.Trace("quitting systray")
	systray.Quit()
}

func getAlarmTime(alarm ical.VAlarm, event ical.VEvent) (time.Time, error) {
	start, err := event.GetStartAt()
	if err != nil {
		return time.Time{}, errors.New("failed to get start time:" + err.Error())
	}

	// parse the alarm time
	alarmOffset, err := parseIcalDuration(alarm.GetProperty(ical.ComponentPropertyTrigger).Value)
	if err != nil {
		return time.Time{}, errors.New("unable to parse alarm trigger time:" + err.Error())
	}
	return start.Add(alarmOffset), nil
}

func fetch() {
	log.Info("fetching calendars")
	myCals := make([]*MyCalendar, 0)
	for _, metaCal := range config.CalendarsMetaData {
		// fetch the data from internet
		ics, err := http.Get(metaCal.Url)
		if err != nil {
			log.Errorf("failed to get ics file from calendar %s, error: %s", metaCal.Name, err.Error())
			continue
		}

		// parse the ical data
		cal, err := ical.ParseCalendar(ics.Body)
		if err != nil {
			log.Errorf("failed to parse calendar %s, error: %s", metaCal.Name, err.Error())
			continue
		}

		// create new MyCalendar from parsed
		newCal := MyCalendar{
			Title:     metaCal.Name,
			Url:       metaCal.Url,
			Events:    make(map[string]*MyEvent),
			ClientCmd: metaCal.OpenClientCmd,
		}
		myCals = append(myCals, &newCal)

		// process all events
		for _, vEvent := range cal.Events() {
			start, err := vEvent.GetStartAt()
			if err != nil {
				if strings.HasPrefix(err.Error(), "unknown time zone") {
					vEvent.GetProperty("DTSTART").ICalParameters = make(map[string][]string)
					start, err = vEvent.GetStartAt()
					if err != nil {
						log.Warnf("failed to get starting time of event %s, error: %s", vEvent.Id(), err.Error())
						continue
					}
				} else {
					log.Warnf("failed to get starting time of event %s, error: %s", vEvent.Id(), err.Error())
				}
			}

			end, err := vEvent.GetEndAt()
			if err != nil {
				//log.Warnf("failed to get ending time of event, setting new end at the end of the day")
				end = time.Date(start.Year(), start.Month(), start.Day(), 23, 59, 0, 0, time.Local)
			}

			// create new MyEvent
			if time.Now().Before(end) {
				newEvent := MyEvent{
					Title:   vEvent.GetProperty(ical.ComponentPropertySummary).Value,
					Id:      vEvent.Id(),
					Start:   start,
					End:     end,
					FullDay: start.Hour() == 0 && start.Minute() == 0 && end.Hour() == 0 && end.Minute() == 0 && end.Sub(start) == 24*time.Hour,
					Alarms:  make([]*MyAlarm, 0),
				}

				// store it
				newCal.Events[newEvent.Id] = &newEvent

				// extract imported alarms
				for _, vAlarm := range vEvent.Alarms() {
					alarmTime, err := getAlarmTime(*vAlarm, *vEvent)
					if err != nil {
						log.Errorf("failed to get alarm time, error: %s", err.Error())
						continue
					}
					newAlarm := MyAlarm{
						Event:   &newEvent,
						Trigger: alarmTime,
					}
					newEvent.Alarms = append(newEvent.Alarms, &newAlarm)
				}

				// add default alarms
				if len(newEvent.Alarms) == 0 || config.ForceDefaultReminders {
					var durationStrings []string
					if newEvent.FullDay {
						durationStrings = metaCal.FullDayNotifications
					} else {
						durationStrings = metaCal.Notifications
					}
					for _, durationString := range durationStrings {
						dur, err := time.ParseDuration(durationString)
						if err != nil {
							log.Errorf("failed to parse %s into duration (from config file), error: %s", durationString, err.Error())
							continue
						}
						newAlarm := MyAlarm{
							Event:   &newEvent,
							Trigger: newEvent.Start.Add(-dur),
						}
						newEvent.Alarms = append(newEvent.Alarms, &newAlarm)
					}
				}
			}
		}
	}
	log.Infof("fetched and parsed %d calendars.", len(myCals))
	lastFetch = time.Now()
	cals = myCals
}

func check(cals []*MyCalendar) (*MyAlarm, *MyEvent) {
	var nearestAlarm *MyAlarm
	var nearestEvent *MyEvent
	for _, cal := range cals {
		for _, event := range cal.Events {
			if event.FullDay {
				for _, alarm := range event.Alarms {
					if alarm.Trigger.YearDay() == time.Now().YearDay() && !alarm.Used {
						notify(alarm)
					}
				}
			} else {
				for _, alarm := range event.Alarms {
					if alarm.Used || time.Now().After(alarm.Trigger) {
						continue
					}
					if nearestAlarm == nil || time.Until(alarm.Trigger) < time.Until(nearestAlarm.Trigger) {
						nearestAlarm = alarm
					}
				}
			}
			if event.Start.After(time.Now()) {
				if nearestEvent == nil || event.Start.Before(nearestEvent.Start) {
					nearestEvent = event
				}
			}
		}
	}
	if nearestAlarm == nil {
		// if no alarm exists, create a dummy one, otherwise nil in return will cause panic in select in main()
		nearestAlarm = &MyAlarm{
			Event:   nil,
			Trigger: time.Now().Add(config.FetchPeriod),
			Used:    false,
		}
	}
	return nearestAlarm, nearestEvent
}

func updateTooltip(event *MyEvent) {
	if event == nil {
		systray.SetTitle("gcald is running\nno events in the future found")
		return
	}
	var msg string
	if event.FullDay {
		msg = fmt.Sprintf("on %d.%d. (fullday)",
			event.Start.Local().Day(),
			event.Start.Local().Month(),
		)
	} else {
		msg = fmt.Sprintf("on %d.%d. at %02d:%02d",
			event.Start.Local().Day(),
			event.Start.Local().Month(),
			event.Start.Local().Hour(),
			event.Start.Local().Minute(),
		)
	}
	tooltip := fmt.Sprintf("next: %s\n%s", event.Title, msg)
	systray.SetTitle(tooltip)
	if runtime.GOOS == "windows" {
		systray.SetTooltip(tooltip)
	}
}

func notify(alarm *MyAlarm) {
	log.Info("creating notification for alarm", alarm.Event.Title)
	if alarm.Event == nil {
		// this is dummy alarm, ignore it
		return
	}
	var msg string
	if alarm.Event.FullDay {
		if _, ok := cache[alarm.Event.Id]; ok {
			return
		}
		cache[alarm.Event.Id] = struct{}{}
		msg = fmt.Sprintf("on %d.%d. (fullday)\nremaining time: %s",
			alarm.Event.Start.Local().Day(),
			alarm.Event.Start.Local().Month(),
			formatApproxDuration(time.Until(alarm.Event.Start)))
	} else {
		msg = fmt.Sprintf("on %d.%d. at %02d:%02d\nremaining time: %s",
			alarm.Event.Start.Local().Day(),
			alarm.Event.Start.Local().Month(),
			alarm.Event.Start.Local().Hour(),
			alarm.Event.Start.Local().Minute(),
			formatApproxDuration(time.Until(alarm.Event.Start)))
	}
	err := beeep.Notify(alarm.Event.Title, msg, "calendar_icon.svg")
	if err != nil {
		log.Infof("failed to display notification for event %s, error: %s", alarm.Event.Title, err.Error())
	}
	alarm.Used = true
}

func importFile(name string) {
	// load-import file with input data
	f, err := ioutil.ReadFile(name)
	if err != nil {
		log.Fatal("failed to read import file:", err)
	}

	// parse imported file
	err = json.Unmarshal(f, &config)
	if err != nil {
		log.Error("failed to unmarshall JSON:", err)
	}

	// parse fetch period from config
	config.FetchPeriod, err = time.ParseDuration(config.FetchPeriodRaw)
	if err != nil {
		log.Error("failed to parse fetch period: ", err)
		config.FetchPeriod = 30 * time.Minute
	}

	if config.ForceCheckPeriod > config.FetchPeriod {
		log.Warn("fetch period is smaller than check period")
	}

	log.Infof("file %s loaded.", name)

	//updateMenuButtons()
}

func updateMenuButtons() {
	systray.ResetMenu()
	mReload := systray.AddMenuItem("Reload config", "Reloads the config file from disk.")
	mFetch := systray.AddMenuItem("Fetch now", "Fetches data from internet.")
	for _, cal := range cals {
		log.Trace("added menu item")
		button := systray.AddMenuItem("Open client: "+cal.Title, "Opens the client with provided command.")
		cal := cal // create independent variable, suggested by Idea
		go func() {
			for range button.ClickedCh {
				log.Trace("opening client")
				_ = exec.Command(strings.Split(cal.ClientCmd, " ")[0], strings.Split(cal.ClientCmd, " ")[1:]...).Run()
			}
		}()
	}
	mQuit := systray.AddMenuItem("Quit", "Quit program.")
	log.Trace("menu icons loaded")

	go func() {
		for range mReload.ClickedCh {
			importFile(dataFolderPath + configFileName)
		}
	}()
	go func() {
		for range mFetch.ClickedCh {
			interruptCh <- struct{}{}
		}
	}()
	go func() {
		for range mQuit.ClickedCh {
			log.Info("quit button clicked, goodbye")
			os.Exit(0)
		}
	}()
}

func formatApproxDuration(dur time.Duration) string {
	if dur.Hours() > 2*24 {
		return fmt.Sprintf("%.0f d ", dur.Hours()/24)
	}
	h := dur.Minutes() / 60
	m := math.Mod(dur.Minutes(), 60)
	if h >= 1 {
		return fmt.Sprintf("%d h %d min", int(h), int(m))
	} else {
		return fmt.Sprintf("%d min", int(m))
	}
}

func parseIcalDuration(s string) (time.Duration, error) {
	units := []string{"S", "M", "H", "D", "W"}
	dur := 0 * time.Second
	for _, unit := range units {
		r := regexp.MustCompile("\\d+" + unit)
		found := r.FindString(s)
		if found == "" {
			continue
		}
		trimmed := found[0 : len(found)-1]
		num, err := strconv.Atoi(trimmed)
		durToParse := ""
		switch unit {
		case "S", "M", "H":
			durToParse = fmt.Sprintf("%d%s", num, strings.ToLower(unit))
		case "D":
			durToParse = fmt.Sprintf("%dh", 24*num)
		case "W":
			durToParse = fmt.Sprintf("%dh", 7*24*num)
		}
		parsed, err := time.ParseDuration(durToParse)
		if err != nil {
			log.Error("failed to parse string", durToParse, "into duration")
			continue
		}
		dur += parsed
	}
	if s[0] == '-' {
		dur *= -1
	}
	if dur == 0 {
		return dur, errors.New("no valid non-zero duration found")
	}
	return dur, nil
}
