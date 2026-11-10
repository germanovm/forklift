package populator

import (
	"testing"
	"time"

	"k8s.io/client-go/kubernetes"
)

// newTestHostLeaseLocker builds a HostLeaseLocker without touching a real
// cluster. The clientset is not used before WithLock, so nil is fine for
// constructor-level tests.
func newTestHostLeaseLocker() *HostLeaseLocker {
	return NewHostLeaseLocker(kubernetes.Interface(nil))
}

func TestNewHostLeaseLockerDefaults(t *testing.T) {
	h := newTestHostLeaseLocker()

	if h.maxConcurrentHolders != 2 {
		t.Errorf("maxConcurrentHolders = %d, want 2", h.maxConcurrentHolders)
	}
	if h.leaseDuration != 10*time.Second {
		t.Errorf("leaseDuration = %v, want 10s", h.leaseDuration)
	}
	if h.namespace != "openshift-mtv" {
		t.Errorf("namespace = %q, want %q", h.namespace, "openshift-mtv")
	}
}

func TestNewHostLeaseLockerMaxHoldersEnv(t *testing.T) {
	tests := []struct {
		name     string
		env      string
		expected int
	}{
		{"empty keeps default", "", 2},
		{"valid value", "5", 5},
		{"minimum boundary", "1", 1},
		{"zero is invalid", "0", 2},
		{"negative is invalid", "-3", 2},
		{"non numeric is invalid", "many", 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("HOST_LEASE_MAX_HOLDERS", tt.env)
			h := newTestHostLeaseLocker()
			if h.maxConcurrentHolders != tt.expected {
				t.Errorf("maxConcurrentHolders = %d, want %d", h.maxConcurrentHolders, tt.expected)
			}
		})
	}
}

func TestNewHostLeaseLeaseDurationEnv(t *testing.T) {
	t.Setenv("HOST_LEASE_DURATION_SECONDS", "30")
	h := newTestHostLeaseLocker()
	if h.leaseDuration != 30*time.Second {
		t.Errorf("leaseDuration = %v, want 30s", h.leaseDuration)
	}

	t.Setenv("HOST_LEASE_DURATION_SECONDS", "not-a-number")
	h = newTestHostLeaseLocker()
	if h.leaseDuration != 10*time.Second {
		t.Errorf("leaseDuration = %v, want default 10s", h.leaseDuration)
	}
}
