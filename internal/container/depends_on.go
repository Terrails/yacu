package container

import (
	"strconv"
	"strings"
)

// A dependency of a container on a compose service, from compose's depends_on label.
type Dependency struct {
	Service   string
	Condition DependencyType
	// whether the dependant is restarted when the service is recreated (depends_on's restart)
	Restart bool
}

// Parses the value of compose's depends_on label, `service:condition:restart` for
// each dependency, comma separated. Compose always writes all three parts. When
// set by hand, a missing or unknown condition means service_healthy and a missing
// or invalid restart means true. Entries without a service are skipped.
func ParseDependsOn(label string) []Dependency {
	var dependencies []Dependency
	for _, entry := range strings.Split(label, ",") {
		parts := strings.Split(entry, ":")
		for i := range parts {
			parts[i] = strings.TrimSpace(parts[i])
		}
		if len(parts[0]) == 0 {
			continue
		}

		dependency := Dependency{Service: parts[0], Condition: DEPENDENCY_HEALTHY, Restart: true}
		if len(parts) > 1 {
			switch DependencyType(strings.ToLower(parts[1])) {
			case DEPENDENCY_STARTED:
				dependency.Condition = DEPENDENCY_STARTED
			case DEPENDENCY_COMPLETED:
				dependency.Condition = DEPENDENCY_COMPLETED
			}
		}
		if len(parts) > 2 {
			if restart, err := strconv.ParseBool(parts[2]); err == nil {
				dependency.Restart = restart
			}
		}
		dependencies = append(dependencies, dependency)
	}
	return dependencies
}
