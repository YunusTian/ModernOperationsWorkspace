package manifest_test

import (
	"errors"
	"testing"

	"github.com/mow/mow/sdk"
	"github.com/mow/mow/sdk/manifest"
)

func TestParseConstraint_WildcardAndEmpty(t *testing.T) {
	for _, s := range []string{"", "*", "  "} {
		c, err := manifest.ParseConstraint(s)
		if err != nil {
			t.Fatalf("ParseConstraint(%q) err = %v", s, err)
		}
		ok, err := c.Check("0.5.0")
		if err != nil || !ok {
			t.Errorf("wildcard %q should match anything, got ok=%v err=%v", s, ok, err)
		}
	}
}

func TestParseConstraint_Errors(t *testing.T) {
	cases := []string{
		"v1.2.3",         // 前导 v 不允许
		">=1.2",          // 非 semver
		">>1.0.0",        // 未知算子
		"1.0.0,,2.0.0",   // 空 term
		">= not-a-ver",   // 版本非法
	}
	for _, c := range cases {
		if _, err := manifest.ParseConstraint(c); err == nil {
			t.Errorf("ParseConstraint(%q) expected error, got nil", c)
		}
	}
}

func TestConstraint_Check_Range(t *testing.T) {
	c, err := manifest.ParseConstraint(">=0.5.0,<0.6.0")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	matches := []string{"0.5.0", "0.5.1", "0.5.99", "0.5.10"}
	misses := []string{"0.4.9", "0.6.0", "1.0.0"}
	for _, v := range matches {
		ok, err := c.Check(v)
		if err != nil || !ok {
			t.Errorf("Check(%q) = (%v, %v), want (true, nil)", v, ok, err)
		}
	}
	for _, v := range misses {
		ok, err := c.Check(v)
		if err != nil || ok {
			t.Errorf("Check(%q) = (%v, %v), want (false, nil)", v, ok, err)
		}
	}
}

func TestConstraint_Check_PreRelease(t *testing.T) {
	c, err := manifest.ParseConstraint(">=0.5.0")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	// pre-release 应低于 base
	if ok, _ := c.Check("0.5.0-rc.1"); ok {
		t.Errorf("0.5.0-rc.1 should not satisfy >=0.5.0")
	}
	if ok, _ := c.Check("0.5.0"); !ok {
		t.Errorf("0.5.0 should satisfy >=0.5.0")
	}
	if ok, _ := c.Check("0.5.1-alpha"); !ok {
		t.Errorf("0.5.1-alpha should satisfy >=0.5.0")
	}
}

func TestConstraint_Check_ExactAndNotEqual(t *testing.T) {
	c, err := manifest.ParseConstraint("=1.0.0")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if ok, _ := c.Check("1.0.0"); !ok {
		t.Errorf("=1.0.0 should match 1.0.0")
	}
	if ok, _ := c.Check("1.0.1"); ok {
		t.Errorf("=1.0.0 should not match 1.0.1")
	}

	c, err = manifest.ParseConstraint("!=1.0.0")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if ok, _ := c.Check("1.0.0"); ok {
		t.Errorf("!=1.0.0 should not match 1.0.0")
	}
	if ok, _ := c.Check("1.0.1"); !ok {
		t.Errorf("!=1.0.0 should match 1.0.1")
	}
}

func TestConstraint_Check_BadInput(t *testing.T) {
	c, err := manifest.ParseConstraint(">=0.5.0")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if _, err := c.Check("not-a-version"); err == nil {
		t.Error("expected error for bogus version")
	}
}

func TestCheckCompatibility_Layers(t *testing.T) {
	m, err := manifest.Parse([]byte(validManifest))
	if err != nil {
		t.Fatalf("parse manifest: %v", err)
	}

	// happy: all three layers satisfy
	if err := m.CheckCompatibility("0.5.2", "0.5.1", "1.1.0"); err != nil {
		t.Fatalf("expected compatible, got: %v", err)
	}

	// core fails
	err = m.CheckCompatibility("0.4.0", "0.5.1", "1.1.0")
	assertIncompatible(t, err, "core", "0.4.0")

	// sdk fails
	err = m.CheckCompatibility("0.5.2", "0.6.0", "1.1.0")
	assertIncompatible(t, err, "sdk", "0.6.0")

	// protocol fails
	err = m.CheckCompatibility("0.5.2", "0.5.1", "0.9.0")
	assertIncompatible(t, err, "protocol", "0.9.0")

	// missing runtime version
	err = m.CheckCompatibility("", "0.5.1", "1.1.0")
	assertIncompatible(t, err, "core", "")
}

func TestCheckCompatibility_SkipEmptyLayer(t *testing.T) {
	m := &manifest.Manifest{
		Compatibility: manifest.Compatibility{Core: ">=0.5.0,<0.6.0"},
	}
	// sdk / protocol 未声明，即使运行时为空也应通过
	if err := m.CheckCompatibility("0.5.2", "", ""); err != nil {
		t.Fatalf("expected compatible, got: %v", err)
	}
}

func assertIncompatible(t *testing.T, err error, wantLayer, wantActual string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var se *sdk.Error
	if !errors.As(err, &se) {
		t.Fatalf("error is not *sdk.Error: %T", err)
	}
	if se.Code != manifest.ErrCodeIncompatible {
		t.Fatalf("code = %q, want %q (message=%s)", se.Code, manifest.ErrCodeIncompatible, se.Message)
	}
	if got, _ := se.Details["layer"].(string); got != wantLayer {
		t.Errorf("Details.layer = %q, want %q", got, wantLayer)
	}
	if got, _ := se.Details["actual"].(string); got != wantActual {
		t.Errorf("Details.actual = %q, want %q", got, wantActual)
	}
}
