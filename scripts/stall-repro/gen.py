#!/usr/bin/env python3
"""Generate synthetic dispatch records for the startup-stall repro (Phase B).

Writes <home>/state/dispatches/<id>.json + .ndjson shaped like production
records, and a throwaway git repo (<home>/repo) with real linked worktrees for
the records that claim one. Never reads or touches ~/.leo.

Mix (fractions of --count, tunable):
  released/kept, no pane, worktree  (production's biggest group)  ~45%
  closed|failed|canceled|done / deferred, no pane, worktree       ~20%
  terminal interactive WITH a (nonexistent) PaneID, worktree      ~15%
  split-viewer, caller window, no pane                            ~10%
  worktree_state=creating                                         ~5%
  no worktree at all                                              ~5%
Of the records that claim a worktree, --present of them exist on disk.
"""
import argparse, json, os, random, subprocess, sys, datetime as dt

p = argparse.ArgumentParser()
p.add_argument("--home", required=True)
p.add_argument("--count", type=int, default=1100)
p.add_argument("--present", type=float, default=0.3)
p.add_argument("--ndjson-kb", type=int, default=20)
p.add_argument("--seed", type=int, default=1)
a = p.parse_args()
rnd = random.Random(a.seed)

home = os.path.abspath(a.home)
if "/.leo" in home and not home.startswith("/private/tmp") and "scratchpad" not in home and "tmp" not in home:
    sys.exit("refusing: home looks like a real leo home")
state = os.path.join(home, "state", "dispatches")
repo = os.path.join(home, "repo")
wts = os.path.join(home, "worktrees")
os.makedirs(state, exist_ok=True); os.makedirs(wts, exist_ok=True)

def git(*args, cwd=repo):
    return subprocess.run(["git", *args], cwd=cwd, check=True, capture_output=True, text=True).stdout.strip()

if not os.path.isdir(os.path.join(repo, ".git")):
    os.makedirs(repo, exist_ok=True)
    git("init", "-q", "-b", "main")
    git("config", "user.email", "bots@blackpaw.studio"); git("config", "user.name", "stall-repro")
    open(os.path.join(repo, "f.txt"), "w").write("x\n")
    git("add", "."); git("commit", "-qm", "base")
base = git("rev-parse", "HEAD")

now = dt.datetime.now(dt.timezone.utc).astimezone()
def ts(minutes_ago): return (now - dt.timedelta(minutes=minutes_ago)).isoformat()

def kind_for(i):
    r = i / a.count
    for name, edge in (("kept", .45), ("deferred", .65), ("pane", .80), ("split", .90), ("creating", .95), ("plain", 1.01)):
        if r < edge: return name

line = json.dumps({"t": 1.0, "d": {"type": "assistant", "text": "x" * 200}}) + "\n"
counts = {}
for i in range(a.count):
    rid = "d-%012x" % (0x5000000000 + i)
    kind = kind_for(i)
    counts[kind] = counts.get(kind, 0) + 1
    start = 60 + i * 5 + rnd.randint(0, 4)
    rec = {
        "id": rid, "caller": "leo", "template": "claude-implementer", "role": "implement",
        "profile": "mixed", "harness": "claude", "model": "sonnet", "kind": "dispatch",
        "cwd": os.path.join(wts, rid), "name": "synthetic-%d" % i, "prompt": "synthetic brief %d" % i,
        "status": "released", "started_at": ts(start), "ended_at": ts(start - 3), "active_seconds": 12.5,
        "running_since": None, "viewer_window_id": "synthetic-%d·%s" % (i, rid[-4:]), "mode": "interactive",
        "session_id": "00000000-0000-4000-8000-%012x" % i, "notify": True,
        "caller_pane_id": "%%%d" % (100 + i), "caller_harness": "claude", "caller_session_id": "$1",
        "caller_window_id": "@%d" % (500 + i % 7), "caller_bridge_key": "leo",
        "notifications": {}, "turns": [{"turn_id": rid + "#1", "source": "orchestrator", "started_at": ts(start),
            "ended_at": ts(start - 3), "delivered": True, "slot_held": False, "outcome": "finished", "text": "ok"}],
    }
    has_wt = kind not in ("plain", "split")
    if has_wt:
        wt = os.path.join(wts, rid)
        rec.update(isolation="worktree", worktree=wt, branch="leo/" + rid, base_commit=base,
                   source_cwd=repo, repository_root=repo)
        exists = kind == "creating" or rnd.random() < a.present
        if exists:
            git("worktree", "add", "-q", "-b", "leo/" + rid, wt, base)
    if kind == "kept":
        rec["worktree_state"] = "kept"
    elif kind == "deferred":
        rec.update(status=rnd.choice(["closed", "failed", "canceled", "done"]), worktree_state="deferred")
    elif kind == "pane":
        rec.update(status=rnd.choice(["failed", "closed", "released"]), worktree_state="kept",
                   pane_id="%%%d" % (9000 + i), viewer_kind="background")
    elif kind == "split":
        rec.update(viewer_kind="split", status="released")
        rec.pop("caller_pane_id")
    elif kind == "creating":
        rec.update(status="running", worktree_state="creating", running_since=ts(start), ended_at=None)
        rec.pop("ended_at")
    with open(os.path.join(state, rid + ".json"), "w") as f: json.dump(rec, f, indent=1)
    with open(os.path.join(state, rid + ".ndjson"), "w") as f:
        f.write(line * max(1, a.ndjson_kb * 1024 // len(line)))

print(json.dumps({"home": home, "count": a.count, "kinds": counts, "base": base}))
