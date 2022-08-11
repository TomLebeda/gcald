Gcald is simple lightweight daemon that keeps an eye on your online calendars and displays desktop notifications about upcoming events.
You don`t need to keep your browser or any other heavy client running in the background anymore.

The program should work on any operating system, but has been tested only on Linux.

# How to set it up

1. Download the sources and build the app with `go build gcald.go`.
2. Write/edit config file (more about config below).
3. That's it, now you just run `./gcald -i [path to folder with config and icons]` or `gcald.exe -i [path to folder with config and icons`

# How does it work

On startup, config file is loaded and calendars are fetched and parsed. Alarms are added to all events based on configuration.

The nearest alarm (among all events in all calendars) is found and program is put to sleep until that time. After it awakes, it displays the notification, finds next nearest alarm
and the cycle repeats.

Calendars are re-fetched from the internet periodically with period specified in config file.

Gcald puts an icon in systray, where user can:

1. manually force re-fetch of all calendars
2. force reload of the config file
3. open any calendar client with given command
4. quit the program

Gcald is read-only, so you can use secret address or any public url that allow you to download ical format. No authentication is required.

# Configuration file

Configuration file is simple JSON file named `gcald_import.json` with following structure:

```
{
  "fetch_period": "30m",
  "force_default_reminders": false,
  "calendars": [
    {
      "name": "My personal calendar",
      "url": "https://calendar.google.com/calendar/ical/someHash/basic.ics",
      "notification_offsets": ["20m","1h","3h","24h","168h"],
      "full_day_notifications_offsets": ["24h","48h","92h"]
      "open_client_cmd": "xdg-open https://calendar.google.com/calendar/u/1/r",
    },
    {
      "name": "My work calendar",
      "url": "https://calendar.google.com/calendar/ical/anotherHash/basic.ics",
      "notifications_offsets": ["30m"],
      "full_day_notifications_offsets": ["24h"]
      "open_client_cmd": "xdg-open https://calendar.google.com/calendar/u/1/r",
    }
  ]
}
```

where:

- `fetch_period` is the period between re-fetching calendars from the internet. Allowed format is number followed by "m" for minutes or "h" for hours.
- `open_client_cmd` is the command that will be executed when user click on the "Open client" button in systray icon. This can be any command, so be careful what you put in there.
- `force_default_reminders` determines whether gcald will always add the default alarms. If set to `false`, default alarms will be added to event only if there are no other alarms
  already (from fetch). If set to `true`, default reminders will be added to all events.
- `url` is the url from where gcald will fetch the calendar.
- `notification_offsets` is the list of durations that determine how long before the event start should the notifications be displayed. Allowed format is number followed by "s" for
  seconds, "m" for minutes and "h" for hours. In the example config it can be read as:
    - `20m` = display notification 20 minutes before the event starts
    - `1h` = display notification 1 hour before the event starts
    - `24h` = display notification 24 hours before the event starts

  If event contains any other alarms already, those alarms will work normally as intended. New alarms will be added depending on the `force_default_reminders` value.

- `full_day_notification_offsets` works exactly the same as `notification_offsets`, but applies only on full-day events.
- `open_client_cmd` is the command that will be executed when you click on the button "Open client" (every calendar can run different command to open different clients). 

  Alarms on full-day events are displayed on startup or after midnight (and the notification stays until user deletes it) so using values smaller than 24h is practically pointless.

Unless you change something in the code, the file should be named `gcald_import.json` and it should be located in a folder you specify with the -i flag. All icon files should be in the same folder as well. 