package archunit

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBuildGraph(t *testing.T) {
	dir := t.TempDir()

	// Create a multi-package project
	pkgADir := filepath.Join(dir, "pkg", "controller")
	pkgBDir := filepath.Join(dir, "pkg", "service")
	pkgCDir := filepath.Join(dir, "pkg", "repository")
	os.MkdirAll(pkgADir, 0755)
	os.MkdirAll(pkgBDir, 0755)
	os.MkdirAll(pkgCDir, 0755)

	// Repository package
	os.WriteFile(filepath.Join(pkgCDir, "repo.go"), []byte(`package repository

type UserRepo struct{}

func (r *UserRepo) FindByID(id int) *User {
	return nil
}

type User struct {
	Name string
}
`), 0644)

	// Service package - depends on repository
	os.WriteFile(filepath.Join(pkgBDir, "service.go"), []byte(`package service

import "example.com/test/pkg/repository"

type UserService struct {
	repo *repository.UserRepo
}

func (s *UserService) GetUser(id int) *repository.User {
	return s.repo.FindByID(id)
}
`), 0644)

	// Controller package - depends on service
	os.WriteFile(filepath.Join(pkgADir, "handler.go"), []byte(`package controller

import "example.com/test/pkg/service"

type UserHandler struct {
	svc *service.UserService
}

func (h *UserHandler) HandleGetUser(id int) {
	h.svc.GetUser(id)
}
`), 0644)

	// go.mod
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte(`module example.com/test

go 1.21
`), 0644)

	g, err := BuildGraph(dir, []string{"./..."})
	if err != nil {
		t.Fatalf("BuildGraph: %v", err)
	}

	if len(g.Packages) < 3 {
		t.Errorf("Expected at least 3 packages, got %d", len(g.Packages))
	}

	// Check dependencies
	svcDeps := g.Deps["example.com/test/pkg/service"]
	if !svcDeps["example.com/test/pkg/repository"] {
		t.Error("service should depend on repository")
	}

	ctrlDeps := g.Deps["example.com/test/pkg/controller"]
	if !ctrlDeps["example.com/test/pkg/service"] {
		t.Error("controller should depend on service")
	}
}

func TestLayerRule(t *testing.T) {
	dir := t.TempDir()

	pkgADir := filepath.Join(dir, "controller")
	pkgBDir := filepath.Join(dir, "service")
	pkgCDir := filepath.Join(dir, "repository")
	os.MkdirAll(pkgADir, 0755)
	os.MkdirAll(pkgBDir, 0755)
	os.MkdirAll(pkgCDir, 0755)

	os.WriteFile(filepath.Join(pkgCDir, "repo.go"), []byte(`package repository
func Find() {}
`), 0644)

	os.WriteFile(filepath.Join(pkgBDir, "service.go"), []byte(`package service
import "example.com/test/repository"
func Use() { repository.Find() }
`), 0644)

	os.WriteFile(filepath.Join(pkgADir, "handler.go"), []byte(`package controller
import "example.com/test/service"
func Handle() { service.Use() }
`), 0644)

	os.WriteFile(filepath.Join(dir, "go.mod"), []byte(`module example.com/test
go 1.21
`), 0644)

	rules := NewRules("test").
		Layers().
		Add("controller", "example.com/test/controller").
		Add("service", "example.com/test/service").
		Add("repository", "example.com/test/repository").
		Build().
		Build()

	violations, err := rules.Check(dir, []string{"./..."})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}

	if len(violations) > 0 {
		t.Errorf("Expected no violations, got %d:", len(violations))
		for _, v := range violations {
			t.Logf("  %s", v)
		}
	}
}

func TestCycleDetection(t *testing.T) {
	// Note: Go doesn't allow circular imports, so we test with a manually built graph
	g := &Graph{
		Packages:    make(map[string]*PackageInfo),
		Deps:        make(map[string]map[string]bool),
		ReverseDeps: make(map[string]map[string]bool),
	}

	// Simulate a cycle: a -> b -> c -> a
	g.Packages["a"] = &PackageInfo{ImportPath: "a"}
	g.Packages["b"] = &PackageInfo{ImportPath: "b"}
	g.Packages["c"] = &PackageInfo{ImportPath: "c"}
	g.Deps["a"] = map[string]bool{"b": true}
	g.Deps["b"] = map[string]bool{"c": true}
	g.Deps["c"] = map[string]bool{"a": true}

	detector := NewCycleDetector()
	violations := detector.Check(g)

	if len(violations) == 0 {
		t.Error("Expected cycle violations, got none")
	}

	for _, v := range violations {
		t.Logf("  %s", v)
	}
}

func TestNamingRules(t *testing.T) {
	dir := t.TempDir()

	pkgDir := filepath.Join(dir, "handler")
	os.MkdirAll(pkgDir, 0755)

	os.WriteFile(filepath.Join(pkgDir, "handler.go"), []byte(`package handler

func HandleUser() {}    // matches
func getInternal() {}   // not exported, should be skipped
func ProcessData() {}   // doesn't match Handle* or Serve*
`), 0644)

	os.WriteFile(filepath.Join(dir, "go.mod"), []byte(`module example.com/test
go 1.21
`), 0644)

	rules := NewRules("test").
		Naming().
		Functions().
		MustMatch("^Handle|^Serve").
		Build()

	violations, err := rules.Check(dir, []string{"./..."})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}

	// ProcessData should violate the rule
	found := false
	for _, v := range violations {
		if v.Rule == "naming-function" {
			found = true
			t.Logf("  %s", v)
		}
	}

	if !found {
		t.Error("Expected naming violation for ProcessData")
	}
}

func TestFluentAPI(t *testing.T) {
	rules := NewRules("myproject").
		That("controller").MayNotDependOn("repository").
		That("service").MayOnlyDependOn("domain", "repository").
		NoCycles().
		Naming().Functions().MustMatch("^Handle|^Serve").
		Build()

	if rules.Name() != "myproject" {
		t.Errorf("Expected name 'myproject', got '%s'", rules.Name())
	}

	if len(rules.rules) == 0 {
		t.Error("Expected rules to be non-empty")
	}
}

func TestMatchPattern(t *testing.T) {
	tests := []struct {
		pkg     string
		pattern string
		want    bool
	}{
		{"github.com/foo/bar", "github.com/foo/bar", true},
		{"github.com/foo/bar", "github.com/foo/*", true},
		{"github.com/foo/bar", "github.com/*/bar", true},
		{"github.com/foo/bar", "github.com/*", false},     // * matches one segment
		{"github.com/foo/bar/baz", "github.com/foo/*", false}, // * matches one segment
		{"github.com/foo/bar/baz", "github.com/foo/*/baz", true},
		{"anything", "*", true},
		{"github.com/foo/bar", "github.com/foo/*/baz", false},
	}

	for _, tt := range tests {
		got := matchPattern(tt.pkg, tt.pattern)
		if got != tt.want {
			t.Errorf("matchPattern(%q, %q) = %v, want %v", tt.pkg, tt.pattern, got, tt.want)
		}
	}
}
