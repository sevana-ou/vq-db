package alarm

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// CommandNotifier runs a rule's shell command on its own goroutine, one at a
// time, so a slow script never holds up the evaluation.
//
// The command line gets the documented placeholders ($r_factor, $sevana_mos,
// $network_mos, $packet_loss, $jitter, $duration, $limit, $free_disk_space)
// replaced by numbers only. Everything else - the alarm's name, state and
// filter, which may contain text an operator did not write - is passed as
// VQ_ALARM_* environment variables, never pasted into the shell line.
type CommandNotifier struct {
	Timeout time.Duration
	queue   chan Event
	done    chan struct{}
	run     func(ctx context.Context, line string, env []string) ([]byte, error)
}

// NewCommandNotifier starts the command worker.
func NewCommandNotifier(timeout time.Duration) *CommandNotifier {
	if timeout <= 0 {
		timeout = time.Minute
	}
	c := &CommandNotifier{Timeout: timeout, queue: make(chan Event, 64), done: make(chan struct{}), run: runShell}
	go c.loop()
	return c
}

// Notify queues the event's command; a full queue drops it with a log line.
func (c *CommandNotifier) Notify(ev Event) {
	if ev.Rule.Command == "" || (ev.Kind == Cleared && !ev.Rule.OnClear) {
		return
	}
	select {
	case c.queue <- ev:
	default:
		slog.Error("alarm command queue full; command dropped", "alarm", ev.Rule.Name, "kind", ev.Kind)
	}
}

// Close stops the worker after the queued commands ran.
func (c *CommandNotifier) Close() {
	close(c.queue)
	<-c.done
}

func (c *CommandNotifier) loop() {
	defer close(c.done)
	for ev := range c.queue {
		ctx, cancel := context.WithTimeout(context.Background(), c.Timeout)
		out, err := c.run(ctx, CommandLine(ev), Env(ev))
		cancel()
		if err != nil {
			slog.Error("alarm command failed", "alarm", ev.Rule.Name, "kind", ev.Kind, "err", err,
				"output", strings.TrimSpace(string(out)))
		}
	}
}

func runShell(ctx context.Context, line string, env []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", line)
	cmd.Env = append(os.Environ(), env...)
	return cmd.CombinedOutput()
}

func num(v float64) string { return strconv.FormatFloat(v, 'f', 3, 64) }

// CommandLine is the rule's command with the documented placeholders filled.
func CommandLine(ev Event) string {
	s := ev.Snapshot
	free := ""
	if s.FreeDisk >= 0 {
		free = num(s.FreeDisk)
	}
	home, _ := os.UserHomeDir()
	r := strings.NewReplacer(
		"$free_disk_space", free,
		"$r_factor", num(s.RFactor),
		"$sevana_mos", num(s.SevanaMOS),
		"$network_mos", num(s.NetworkMOS),
		"$packet_loss", num(s.PacketLoss),
		"$jitter", num(s.Jitter),
		"$duration", num(s.Duration),
		"$limit", num(ev.Rule.Limit),
	)
	line := r.Replace(ev.Rule.Command)
	if home != "" && strings.HasPrefix(line, "~/") {
		line = home + line[1:] // as the C++ manager expanded a leading ~
	}
	return line
}

// Env is the event as VQ_ALARM_* environment variables.
func Env(ev Event) []string {
	s := ev.Snapshot
	env := []string{
		"VQ_ALARM_NAME=" + ev.Rule.Name,
		"VQ_ALARM_STATE=" + string(ev.Kind),
		"VQ_ALARM_COUNTER=" + ev.Rule.Counter,
		"VQ_ALARM_VALUE=" + num(ev.Value),
		"VQ_ALARM_OP=" + ev.Rule.Op.String(),
		"VQ_ALARM_LIMIT=" + num(ev.Rule.Limit),
		"VQ_ALARM_UNIT=" + ev.Rule.Unit,
		"VQ_ALARM_SAMPLES=" + strconv.Itoa(ev.Samples),
		"VQ_ALARM_WINDOW_S=" + strconv.Itoa(int(ev.Rule.Window.Seconds())),
		"VQ_ALARM_FILTER=" + ev.Rule.Filter,
		"VQ_ALARM_CODE=" + ev.Rule.Code,
		"VQ_ALARM_TIME=" + ev.At.UTC().Format(time.RFC3339),
		"VQ_INSTANCE_ID=" + ev.Instance.ID,
		"VQ_INSTANCE_NAME=" + ev.Instance.Name,
		"VQ_STREAMS=" + strconv.Itoa(s.Streams),
		"VQ_R_FACTOR=" + num(s.RFactor),
		"VQ_SEVANA_MOS=" + num(s.SevanaMOS),
		"VQ_NETWORK_MOS=" + num(s.NetworkMOS),
		"VQ_PACKET_LOSS=" + num(s.PacketLoss),
		"VQ_JITTER=" + num(s.Jitter),
		"VQ_DURATION=" + num(s.Duration),
	}
	if s.FreeDisk >= 0 {
		env = append(env, "VQ_FREE_DISK_SPACE="+num(s.FreeDisk))
	}
	return env
}
