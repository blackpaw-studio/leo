package observe

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sync"
	"testing"
	"time"
)

var (
	surfaceT0 = time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	surfaceT1 = surfaceT0.Add(time.Minute)
)

var uuidV4 = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func fixedSurfaceClock(t time.Time) func() time.Time { return func() time.Time { return t } }

func TestSurfacedFileAddRecordsAndPublishesSameObject(t *testing.T) {
	rec := &recordingPublisher{}
	at := time.Date(2026, 9, 24, 13, 0, 0, 0, time.UTC)
	s := NewSurfacedFileStore(rec, fixedSurfaceClock(at))

	got, err := s.Add("alpha", surfaceT0, SurfaceInput{Path: "a.go", AbsPath: "/w/a.go", Line: 3, Reason: "look"})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if got.ID == "" || got.Type != EventFileSurfaced || got.Agent != "alpha" || !got.StartedAt.Equal(surfaceT0) || !got.At.Equal(at) {
		t.Fatalf("Add returned %+v", got)
	}
	if stored := s.Get("alpha"); len(stored) != 1 || stored[0] != got {
		t.Fatalf("Get = %+v, want [%+v]", stored, got)
	}
	events := rec.events
	if len(events) != 1 || events[0].Type != EventFileSurfaced {
		t.Fatalf("published %+v", events)
	}
	payload := events[0].Payload.(*FileSurfacedPayload)
	if payload.SurfacedFile != got {
		t.Fatalf("payload %+v != state %+v", payload.SurfacedFile, got)
	}
}

func TestSurfacedFileIDsAreUUIDv4AndUnique(t *testing.T) {
	s := NewSurfacedFileStore(nil, nil)
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		f, err := s.Add("a", surfaceT0, SurfaceInput{Path: "p", AbsPath: "/p"})
		if err != nil {
			t.Fatal(err)
		}
		if !uuidV4.MatchString(f.ID) {
			t.Fatalf("id %q is not a UUIDv4", f.ID)
		}
		if seen[f.ID] {
			t.Fatalf("duplicate id %q", f.ID)
		}
		seen[f.ID] = true
	}
}

func TestSurfacedFileBusStampDoesNotOverwriteAt(t *testing.T) {
	bus := NewBus()
	ch, cancel, _ := bus.Subscribe(4)
	defer cancel()
	at := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	s := NewSurfacedFileStore(bus, fixedSurfaceClock(at))

	f, err := s.Add("a", surfaceT0, SurfaceInput{Path: "p", AbsPath: "/p"})
	if err != nil {
		t.Fatal(err)
	}
	ev := <-ch
	payload := ev.Payload.(*FileSurfacedPayload)
	if !payload.At.Equal(at) || !payload.At.Equal(f.At) {
		t.Fatalf("bus overwrote at: payload %v, state %v, want %v", payload.At, f.At, at)
	}
	if payload.Seq == 0 {
		t.Fatal("bus did not stamp seq")
	}
}

func TestSurfacedFilePayloadGoldenJSON(t *testing.T) {
	payload := FileSurfacedPayload{Seq: 7, SurfacedFile: SurfacedFile{
		Type: EventFileSurfaced, Agent: "alpha", StartedAt: surfaceT0,
		ID: "0b6f7c4e-8f7e-4c55-9d7a-1f2e3d4c5b6a", Path: "dir with space/ü.go", AbsPath: "/w/dir with space/ü.go",
		Line: 12, Reason: "why", At: surfaceT1,
	}}
	got, err := json.Marshal(&payload)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"seq":7,"type":"file_surfaced","agent":"alpha","started_at":"2026-09-24T12:00:00Z","id":"0b6f7c4e-8f7e-4c55-9d7a-1f2e3d4c5b6a","path":"dir with space/ü.go","abs_path":"/w/dir with space/ü.go","line":12,"reason":"why","at":"2026-09-24T12:01:00Z"}`
	if string(got) != want {
		t.Fatalf("payload JSON\n got %s\nwant %s", got, want)
	}

	bare, err := json.Marshal(SurfacedFile{Type: EventFileSurfaced, Agent: "a", StartedAt: surfaceT0, ID: "x", Path: "p", AbsPath: "/p", At: surfaceT1})
	if err != nil {
		t.Fatal(err)
	}
	wantBare := `{"type":"file_surfaced","agent":"a","started_at":"2026-09-24T12:00:00Z","id":"x","path":"p","abs_path":"/p","at":"2026-09-24T12:01:00Z"}`
	if string(bare) != wantBare {
		t.Fatalf("unset line/reason must be omitted\n got %s\nwant %s", bare, wantBare)
	}
}

func TestSurfacedFileOrderingAndCap(t *testing.T) {
	for _, n := range []int{MaxSurfacedFiles, MaxSurfacedFiles + 1} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			s := NewSurfacedFileStore(nil, nil)
			for i := 0; i < n; i++ {
				if _, err := s.Add("a", surfaceT0, SurfaceInput{Path: fmt.Sprint(i), AbsPath: "/x"}); err != nil {
					t.Fatal(err)
				}
			}
			got := s.Get("a")
			if len(got) != MaxSurfacedFiles {
				t.Fatalf("len = %d, want %d", len(got), MaxSurfacedFiles)
			}
			first := n - MaxSurfacedFiles
			for i, f := range got {
				if f.Path != fmt.Sprint(first+i) {
					t.Fatalf("got[%d].Path = %q, want %d (newest last)", i, f.Path, first+i)
				}
			}
		})
	}
}

func TestSurfacedFileAgentsAreIsolated(t *testing.T) {
	s := NewSurfacedFileStore(nil, nil)
	_, _ = s.Add("a", surfaceT0, SurfaceInput{Path: "a", AbsPath: "/a"})
	_, _ = s.Add("b", surfaceT0, SurfaceInput{Path: "b", AbsPath: "/b"})
	s.Remove("a")

	if got := s.Get("a"); len(got) != 0 {
		t.Fatalf("a = %+v after Remove", got)
	}
	if got := s.Get("b"); len(got) != 1 || got[0].Path != "b" {
		t.Fatalf("b = %+v", got)
	}
	all := s.All()
	if _, ok := all["a"]; ok || len(all) != 1 {
		t.Fatalf("All = %+v", all)
	}
}

func TestSurfacedFileReturnsDefensiveCopies(t *testing.T) {
	s := NewSurfacedFileStore(nil, nil)
	_, _ = s.Add("a", surfaceT0, SurfaceInput{Path: "a", AbsPath: "/a"})

	got := s.Get("a")
	got[0].Path = "mutated"
	all := s.All()
	all["a"][0].Path = "mutated"

	if s.Get("a")[0].Path != "a" {
		t.Fatal("caller mutation leaked into the store")
	}
}

func TestSurfacedFileResetOnNewIncarnation(t *testing.T) {
	s := NewSurfacedFileStore(nil, nil)
	_, _ = s.Add("a", surfaceT0, SurfaceInput{Path: "old", AbsPath: "/old"})

	s.Reset("a", surfaceT0)
	if len(s.Get("a")) != 1 {
		t.Fatal("Reset with the same incarnation must not clear")
	}
	s.Reset("a", surfaceT1)
	if got := s.Get("a"); len(got) != 0 {
		t.Fatalf("files survived a new incarnation: %+v", got)
	}
}

func TestSurfacedFileAddRejectsStaleIncarnation(t *testing.T) {
	s := NewSurfacedFileStore(nil, nil)
	s.Reset("a", surfaceT1)

	if _, err := s.Add("a", surfaceT0, SurfaceInput{Path: "late", AbsPath: "/late"}); !errors.Is(err, ErrStaleIncarnation) {
		t.Fatalf("Add with older incarnation err = %v, want ErrStaleIncarnation", err)
	}
	if got := s.Get("a"); len(got) != 0 {
		t.Fatalf("stale submission stored: %+v", got)
	}
}

func TestSurfacedFileAddWithNewerIncarnationDropsOlder(t *testing.T) {
	s := NewSurfacedFileStore(nil, nil)
	_, _ = s.Add("a", surfaceT0, SurfaceInput{Path: "old", AbsPath: "/old"})
	_, _ = s.Add("a", surfaceT1, SurfaceInput{Path: "new", AbsPath: "/new"})

	got := s.Get("a")
	if len(got) != 1 || got[0].Path != "new" {
		t.Fatalf("got %+v, want only the new incarnation's file", got)
	}
	s.Reset("a", surfaceT0)
	if len(s.Get("a")) != 1 {
		t.Fatal("an older Reset must not roll the incarnation back")
	}
}

func TestSurfacedFileNilStoreIsSafe(t *testing.T) {
	var s *SurfacedFileStore
	s.Reset("a", surfaceT0)
	s.Remove("a")
	if s.Get("a") != nil || s.All() != nil {
		t.Fatal("nil store returned data")
	}
	if _, err := s.Add("a", surfaceT0, SurfaceInput{}); err == nil {
		t.Fatal("nil store Add must fail")
	}
}

func TestSurfacedFileConcurrentUse(t *testing.T) {
	s := NewSurfacedFileStore(NewBus(), nil)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			agent := fmt.Sprint("agent", g%3)
			for i := 0; i < 100; i++ {
				_, _ = s.Add(agent, surfaceT0.Add(time.Duration(i/50)*time.Second), SurfaceInput{Path: "p", AbsPath: "/p"})
				_ = s.All()
				_ = s.Get(agent)
				if i%33 == 0 {
					s.Reset(agent, surfaceT0.Add(time.Duration(i)*time.Second))
				}
				if i%47 == 0 {
					s.Remove(agent)
				}
			}
		}(g)
	}
	wg.Wait()
	for name, files := range s.All() {
		if len(files) > MaxSurfacedFiles {
			t.Fatalf("%s holds %d files", name, len(files))
		}
	}
}
