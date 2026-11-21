# Connection events

What this suite proves, on one Mixer and one Rack:

- A terminal that sends nothing gets a logon when it connects, and each
  connection gets its own.
- A Rack parks an event while the Mixer is away, and the logon leaves when the
  Mixer returns inside `max_event_age`.
- An event older than `max_event_age` is dropped, and the next connection works.
- A client gear publishes the same event, on each dial.

Run it with `make test-robot-conn-events`.

- With two Racks on one host (`two_racks.robot`), a gear that runs on both sees
  only its own connections, and a reader on the other Rack gets what it missed
  inside `max_event_age`, and sends no logon past it.

The unit tests of `pkg/events` cover two readers of one event, the Rack in the
subject, redelivery, and a deleted consumer.
