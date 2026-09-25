package container

import (
	"reflect"
	"testing"
)

func TestParseDependsOn(t *testing.T) {
	tests := []struct {
		name  string
		label string
		want  []Dependency
	}{
		{"as written by compose", "db:service_healthy:true,cache:service_started:false,migrate:service_completed_successfully:true", []Dependency{
			{"db", DEPENDENCY_HEALTHY, true},
			{"cache", DEPENDENCY_STARTED, false},
			{"migrate", DEPENDENCY_COMPLETED, true},
		}},
		{"service only", "db", []Dependency{{"db", DEPENDENCY_HEALTHY, true}}},
		{"without restart", "db:service_started", []Dependency{{"db", DEPENDENCY_STARTED, true}}},
		{"condition in upper case", "db:SERVICE_STARTED:true", []Dependency{{"db", DEPENDENCY_STARTED, true}}},
		{"unknown condition", "db:service_ready:true", []Dependency{{"db", DEPENDENCY_HEALTHY, true}}},
		{"invalid restart", "db:service_started:sometimes", []Dependency{{"db", DEPENDENCY_STARTED, true}}},
		{"restart false", "db:service_healthy:false", []Dependency{{"db", DEPENDENCY_HEALTHY, false}}},
		{"spaces around parts", " db : service_started : false , cache", []Dependency{
			{"db", DEPENDENCY_STARTED, false},
			{"cache", DEPENDENCY_HEALTHY, true},
		}},
		{"empty entries", ",db:service_started:true,,:service_started:true", []Dependency{{"db", DEPENDENCY_STARTED, true}}},
		{"empty label", "", nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ParseDependsOn(test.label); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("got %+v, want %+v", got, test.want)
			}
		})
	}
}
