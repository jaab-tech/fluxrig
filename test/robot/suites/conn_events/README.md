# Connection events

What this suite proves, on one Mixer and one Rack:

- A terminal that sends nothing gets a logon when it connects, and each
  connection gets its own.
- A Rack parks an event while the Mixer is away, and the logon leaves when the
  Mixer returns inside `max_event_age`.
- An event older than `max_event_age` is dropped, and the next connection works.
- A client gear publishes the same event, on each dial.

Run it with `make test-robot-conn-events`.

The unit tests of `pkg/events` cover two readers of one event, the Rack in the
subject, redelivery, and a deleted consumer. A reader on another Rack, and a gear
that runs on two Racks, need two Racks on one host. They were run by hand.
