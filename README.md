# HEOS broker for Martin's home automation

This broker talks to a [HEOS](https://www.denon-hifi.ch/chg/heos) capable device and uses [MQTT](http://mqtt.org/). It's part of [Martin's home automation](https://github.com/martins-home-automation/libs), but can also be used independently.

## Installation

The broker can be run on any host where the HEOS capable device is reachable over the network.
To run the broker execute this:

```
broker-heos -c path/to/config.yaml
```

## Environment variables

The broker connects to the HEOS capable device with a TCP connection. The address of the device must be set in the environment variable `HEOS_URI`.

## Configuration

The configuration file looks like this:

```yaml
topics:
  system: heos/system/set
  playback_state: heos/playback/state
  play: heos/play
  system_state: heos/system/state
  state: heos/state
  info: heos/info
```

### System

The topic defined in `system` can be used to change the state of the broker.
The payload is a string of either `connect` or `disconnect`.

When starting the broker it tries to connect to the HEOS capable device immediately.
When the HEOS capable device shuts down the broker will recognize it automatically and will disconnect.
During the runtime of the broker it **never** tries to connect to the HEOS capable device until `connect` is sent to the topic defined in `system`.

### System state

The topic defined in `system_state` is used to publish the current state of the broker.
The payload is a string of either `connected` or `disconnected`.

The messages published to MQTT are retained.

### Playback state

The topic defined in `playback_state` can be used to change the state of the playback in the HEOS capable device.
The payload is a string of either `play`, `pause`, `stop`, `previous` or `next`.

If the broker is not connected the message will be ignored.

### Play

The base topic defined in `play` can be used to play things in the HEOS capable device.

If the broker is not connected the message will be ignored.

#### Station

Playing a station (radio) is handled in the topic `[play]/station`.
The payload is a string which defines the name of the station.

Internally the service `TuneIn` on the HEOS capable device is used to search for the given station.

### State

The topic defined in `state` is used to publish the current state/playback of the HEOS capable device.

The messages published to MQTT are retained.

### Info

The topic defined in `info` is used to publish general information about the device.
The payload is a JSON object as in the following example:

```
{
  "model": "",
  "version": "",
  "serialnumber": ""
}
```

The messages published to MQTT are retained.
