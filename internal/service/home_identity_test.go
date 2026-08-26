package service

import "testing"

func TestIsDefaultHomeMatchesVariants(t *testing.T) {
	tmp := t.TempDir()

	if !isDefaultHome(tmp, tmp) {
		t.Errorf("isDefaultHome(%q, %q) = false, want true", tmp, tmp)
	}
	if !isDefaultHome(tmp+"/", tmp) {
		t.Errorf("trailing slash should still match default home")
	}
	if !isDefaultHome(tmp+"/./", tmp) {
		t.Errorf("relative-looking suffix should still match default home")
	}
}

func TestIsDefaultHomeDiffers(t *testing.T) {
	a := t.TempDir()
	b := t.TempDir()

	if isDefaultHome(a, b) {
		t.Errorf("isDefaultHome(%q, %q) = true, want false (different homes)", a, b)
	}
}

func TestIsDefaultHomeEmptyDefault(t *testing.T) {
	if isDefaultHome(t.TempDir(), "") {
		t.Error("isDefaultHome with empty defaultHome should never match")
	}
}

func TestHomeIdentityHashStableAndDistinct(t *testing.T) {
	a := t.TempDir()
	b := t.TempDir()

	h1 := homeIdentityHash(a)
	h2 := homeIdentityHash(a)
	if h1 != h2 {
		t.Errorf("homeIdentityHash(%q) not stable: %q != %q", a, h1, h2)
	}
	if h1 == "" || len(h1) != homeIdentityHashLen {
		t.Errorf("homeIdentityHash(%q) = %q, want length %d", a, h1, homeIdentityHashLen)
	}

	h3 := homeIdentityHash(b)
	if h1 == h3 {
		t.Errorf("homeIdentityHash for different homes collided: %q", h1)
	}
}

func TestHomeIdentityHashSpellingVariantsMatch(t *testing.T) {
	tmp := t.TempDir()
	if homeIdentityHash(tmp) != homeIdentityHash(tmp+"/") {
		t.Error("trailing slash should not change the identity hash")
	}
}
