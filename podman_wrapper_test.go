package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

func TestPodmanWrapperArgs(t *testing.T) {
	cases := []struct {
		name       string
		args, want []string
	}{
		{"buildx", []string{"buildx", "build", "."}, []string{"buildx", "build", "--http-proxy=false", "."}},
		{"buildx explicit", []string{"--connection=test", "buildx", "build", "--http-proxy=true", "."}, []string{"--connection=test", "buildx", "build", "--http-proxy=false", "--http-proxy=true", "."}},
		{"other buildx", []string{"buildx", "version"}, nil},
		{"buildx argument", []string{"run", "alpine", "buildx", "build"}, nil},
		{"nested group", []string{"image", "buildx", "build"}, nil},
		{"build", []string{"build", "."}, []string{"build", "--http-proxy=false", "."}},
		{"image build", []string{"image", "build", "-t", "test", "."}, []string{"image", "build", "--http-proxy=false", "-t", "test", "."}},
		{"explicit true", []string{"build", "--http-proxy=true", "."}, []string{"build", "--http-proxy=false", "--http-proxy=true", "."}},
		{"explicit bare", []string{"build", "--http-proxy", "."}, []string{"build", "--http-proxy=false", "--http-proxy", "."}},
		{"explicit false", []string{"build", "--http-proxy=false", "."}, []string{"build", "--http-proxy=false", "--http-proxy=false", "."}},
		{"value looks like proxy", []string{"build", "--build-arg", "--http-proxy=true", "."}, []string{"build", "--http-proxy=false", "--build-arg", "--http-proxy=true", "."}},
		{"context looks like flag", []string{"build", "--", "--http-proxy=true"}, []string{"build", "--http-proxy=false", "--", "--http-proxy=true"}},
		{"global flags", []string{"--remote", "--connection", "test", "build", "."}, []string{"--remote", "--connection", "test", "build", "--http-proxy=false", "."}},
		{"global attached", []string{"-ctest", "--remote=true", "image", "build", "."}, []string{"-ctest", "--remote=true", "image", "build", "--http-proxy=false", "."}},
		{"global equal", []string{"--connection=test", "build", "."}, []string{"--connection=test", "build", "--http-proxy=false", "."}},
		{"global between image build", []string{"image", "--connection", "test", "build", "."}, []string{"image", "--connection", "test", "build", "--http-proxy=false", "."}},
		{"global value build", []string{"--connection", "build", "info"}, nil},
		{"unknown global", []string{"--future-option", "build", "info"}, nil},
		{"unknown equal global", []string{"--future-option=value", "build", "."}, nil},
		{"missing global value", []string{"--connection"}, nil},
		{"help", []string{"help", "build"}, nil},
		{"help option", []string{"--help", "build"}, nil},
		{"other image", []string{"image", "inspect", "build"}, nil},
		{"run", []string{"run", "alpine", "build"}, nil},
		{"pull", []string{"pull", "image"}, nil},
		{"double dash", []string{"--", "build", "."}, nil},
		{"empty", nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want := tc.want
			if want == nil {
				want = tc.args
			}
			before := append([]string(nil), tc.args...)
			if got := podmanWrapperArgs(tc.args); !reflect.DeepEqual(got, want) {
				t.Fatalf("got %#v, want %#v", got, want)
			}
			if !reflect.DeepEqual(tc.args, before) {
				t.Fatal("modified caller arguments")
			}
		})
	}
}

func TestPreparePodmanWrapper(t *testing.T) {
	realDir := t.TempDir()
	name := "podman"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	real := filepath.Join(realDir, name)
	if err := os.WriteFile(real, []byte("fake executable"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", realDir)
	directory := t.TempDir()
	bin, err := preparePodmanWrapper(directory)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(bin, podmanWrapperMetadata))
	if err != nil {
		t.Fatal(err)
	}
	var config podmanWrapperConfig
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	if config.Executable != real {
		t.Fatalf("saved %q, want %q", config.Executable, real)
	}
	wrapper, err := os.Stat(filepath.Join(bin, name))
	if err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	original, err := os.Stat(self)
	if err != nil {
		t.Fatal(err)
	}
	if wrapper.Size() != original.Size() {
		t.Fatal("wrapper is not a full executable copy")
	}
	if runtime.GOOS != "windows" && wrapper.Mode().Perm() != 0700 {
		t.Fatalf("wrapper mode %v", wrapper.Mode())
	}
	if _, err := preparePodmanWrapper(directory); err == nil {
		t.Fatal("replaced existing wrapper directory")
	}
}
