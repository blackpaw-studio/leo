package cli

import (
	"errors"
	"os"
	"reflect"
	"testing"

	"github.com/blackpaw-studio/leo/internal/managed"
	"github.com/blackpaw-studio/leo/internal/tmux"
)

func doctorManagedState() managed.State {
	return managed.State{Managed: true, Marker: managed.Marker{
		Manager:      "Leo.app",
		LaunchdLabel: managedTestLabel,
		SelfPath:     "/h/.leo/app/bin/leo",
		TmuxPath:     "/h/.leo/app/tmux/3.6a/tmux",
	}}
}

func healthyManagedDoctorDeps() managedDoctorDeps {
	return managedDoctorDeps{
		jobStatus: func(string) (string, error) { return "running (pid 4242)", nil },
		readlink:  func(string) (string, error) { return "/Applications/Leo.app/Contents/Helpers/leo", nil },
		tmuxVersion: func(string) (string, error) {
			return "tmux 3.6a", nil
		},
		resolve: func(tmux.ResolveOptions) tmux.Resolution {
			return tmux.Resolution{Client: "/h/.leo/app/tmux/3.6a/tmux", Start: "/h/.leo/app/tmux/3.6a/tmux"}
		},
		cliJobLoaded: func(string) bool { return false },
		cliLabel:     "com.blackpaw.leo",
	}
}

func lines(ls []doctorLine) []string {
	var out []string
	for _, l := range ls {
		out = append(out, l.Level+" "+l.Text)
	}
	return out
}

func TestManagedDoctorHealthyReport(t *testing.T) {
	got := lines(managedDoctorReport(doctorManagedState(), healthyManagedDoctorDeps()))

	want := []string{
		"ok Managed:        yes (Leo.app, launchd job studio.blackpaw.leo.daemon)",
		"ok   Job:          running (pid 4242)",
		"info   self_path:    /h/.leo/app/bin/leo -> /Applications/Leo.app/Contents/Helpers/leo",
		"info   tmux_path:    /h/.leo/app/tmux/3.6a/tmux (tmux 3.6a)",
		"info   tmux client:  /h/.leo/app/tmux/3.6a/tmux; new servers start with /h/.leo/app/tmux/3.6a/tmux",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("lines =\n%q\nwant\n%q", got, want)
	}
}

func TestManagedDoctorUnmanagedIsOneLine(t *testing.T) {
	got := lines(managedDoctorReport(managed.State{}, healthyManagedDoctorDeps()))

	if want := []string{"info Managed:        no"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("lines = %q", got)
	}
}

func TestManagedDoctorShowsAnIgnoredMarkersWarning(t *testing.T) {
	got := lines(managedDoctorReport(managed.State{Warning: "managed.json ignored: bad version"}, healthyManagedDoctorDeps()))

	if want := []string{"warn Managed:        no (managed.json ignored: bad version)"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("lines = %q", got)
	}
}

func TestManagedDoctorFlagsEachProblem(t *testing.T) {
	d := healthyManagedDoctorDeps()
	d.jobStatus = func(string) (string, error) { return "not registered", nil }
	d.readlink = func(string) (string, error) { return "", os.ErrNotExist }
	d.tmuxVersion = func(string) (string, error) { return "tmux 3.5a", nil }
	d.resolve = func(tmux.ResolveOptions) tmux.Resolution {
		return tmux.Resolution{
			Client:  "/h/.leo/app/tmux/3.5a/tmux",
			Start:   "/h/.leo/app/tmux/3.6a/tmux",
			Pending: "tmux upgrade pending: server runs 3.5a",
			Err:     errors.New("no copy for 3.4"),
		}
	}
	d.cliJobLoaded = func(string) bool { return true }

	got := lines(managedDoctorReport(doctorManagedState(), d))

	want := []string{
		"ok Managed:        yes (Leo.app, launchd job studio.blackpaw.leo.daemon)",
		"warn   Job:          not registered",
		"info   self_path:    /h/.leo/app/bin/leo (not a symlink)",
		"warn   tmux_path:    /h/.leo/app/tmux/3.6a/tmux reports tmux 3.5a but sits in directory 3.6a",
		"info   tmux client:  /h/.leo/app/tmux/3.5a/tmux; new servers start with /h/.leo/app/tmux/3.6a/tmux",
		"warn   tmux upgrade pending: server runs 3.5a",
		"warn   tmux error:   no copy for 3.4",
		"warn   CLI job:      com.blackpaw.leo is still loaded; its daemon loses the daemon.lock race and respawns every 10s (launchctl bootout it)",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("lines =\n%q\nwant\n%q", got, want)
	}
}
