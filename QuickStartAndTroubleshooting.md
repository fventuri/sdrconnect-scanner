# Quick start guide to sdrconnect-scanner

For 99.9% of the users and especially for those who start using sdrconnect-scanner, I recommend to create a profile in SDRconnect, and just use that in the configuration file.

There are several other SDRconnect settings that can be configured via sdrconnect-scanner (like device serial, sample rate, etc), but using just a SDRconnect profile is way easier, since a profile contains those settings and many more, like the antenna selection.

These are step-by-step instructions on how to run sdrconnect-scanner for scanning the FM BC band here in the US (88.1MHz to 107.9MHz in 200kHz spacing).

1. Download and install the latest version of SDRconnect (1.0.7 or newer)

2. Start SDRconnect, click on the icon with three dots (...) on the top right of the 'Primary SP' windows, and select 'Preferences'. In the 'Preferences' menu, enable 'WebSocket Server'. Clock 'OK' to exit that menu.

3. In SDRconnect tune to a station in the FM BC band, make sure it sounds OK (you may need to switch antenna), and adjust gains, AGC, squelch, and any other settings as you prefer for the best reception.

4. Once you are happy with that configuration, click on 'Profiles' at the very bottom right of SDRconnect, and save all these settings into a new profile, let's say 'FM-BC'

5. (optional) To validate that the 'FM-BC' profile has been successfully saved, exit SDRconnect, restart it, and say tune it to a ham band. Then click on 'Profiles', and double click on the 'FM-BC' profile. SDRconnect should switch to the FM band in WFM mode, and you should hear the same station as you were when you saved that profile. If you have any problem in this step, don't do the next steps until the issue is fixed.

6. Leave SDRconnect up and running.

7. Download the latest release of sdrconnect-scanner from here: https://github.com/fventuri/sdrconnect-scanner/releases, and install it somewhere in your path

8. Create a text file called 'FM-BC-scan.conf' with these lines:
```
detect power threshold = -70
detect snr threshold = 5
detect time = 600
listen time = 5000

# FM BC scan
[scan]
range = 87.9e6, 107.9e6, 200e3
profile = FM-BC
```

Save the file as 'FB-BC-scan.conf'

9. Open a terminal window, change directory to the one where you saved that configuration file, and run this command:
```
sdrconnect-scanner -conf FM-BC-scan.conf
```

SDRconnect should start scanning through the FM BC stations, and it will pause for 5 seconds every time it finds a new station.

10. If the scanner is too sensitive, i.e. it stops even where there are no stations, edit the configuration file 'FM-BC-scan.conf' and increase the value in the line 'detect snr threshold' to make it less sensitive. Conversely, if you want to make it more sensitive, decrease the value in the 'detect snr threshold' line. After each change to the configuration file, you have to stop and restart sdrconnect-scanner to pick up the changes.

11. If you want to pause for longer than 5 seconds when a station is found, edit 'FM-BC-scan.conf' and increase the value in the line 'listen time'. This value is in ms, so to listen for 10s change that line to 'listen time = 10000'. Save and restart sdrconnect-scanner.

12. While running, sdrconnect-scanner accepts a few commands from the keyboard: 'space' pauses the scanner to allow to listen to station for longer; another 'space' resumes scanning; 'q' or Ctrl-C terminates sdrconnect-scanner, and 'n' moves to the next '[scan]' section, if there are more than one in the configuration file.


## Labels

The default output of sdrconnect-scanner is just a timestamp, frequency, some stats about signal power and signal SNR, and possibly the RDS PI and the RDS PS messages for FM BC stations.
It can be enriched by adding labels, which are just strings associated with a frequency or a RDS PI code. They can be anything, but typically they would be the call sign and perhaps the location of the station on that frequency (or using that RDS PI code).

The are the steps to add labels to the output of sdrconnect-scanner to make it more informative:

1. Create a CSV file called say 'labels.csv' with just lines in the format 'frequency,label'. For instance:
```
10000000,WWV
```
If you also want to add the location, say 'CITY, STATE', then the label needs to be in double quotes, for instance:
```
10000000,"WWV - Fort Collins, CO"
```

An example of CSV file with labels is in [examples/labels.csv](examples/labels.csv).

2. Run sdrconnect-scanner with this command:
```
sdrconnect-scanner -conf FM-BC-scan.conf -labels labels.csv
```

Now whenever sdrconnect-scanner finds a station on a frequency (or RDS PI) that is in the labels file, it will display the associated label to help identify that station.


## Troubleshooting

### Error message: dial tcp 127.0.0.1:5454: connect: connection refused

This message means that there's nothing on the computer listening on the WebSocket port 5454.
This could be due to a couple of reasons:
  - SDRconnect is not running
  - SDRconnect is running but WebSockets Server is not enabled; follow the instructions in step 2 above.
