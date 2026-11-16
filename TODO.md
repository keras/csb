# TODO

## Host exec across detach

`--host-exec` starts the broker as a child of the `csb` process, so with
`--detachable` it dies as soon as you detach, and `csb attach` does not bring
it back. Host commands (`csb-host-run`) stop working in a still-running session.

Ideas:
- Let the broker outlive csb: move it out of csb's process group and have it
  watch `<runtime> wait <container>` instead of the stdin deadman pipe, exiting
  when the container does.
- Or have `csb attach` start a fresh broker and push its new URL and token into
  the running container (e.g. a file the `csb-host-run` client reads), instead
  of baking them into the container's env at `run` time.
