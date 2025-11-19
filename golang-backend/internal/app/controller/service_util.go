package controller

import (
    "encoding/json"
)

// expandRUDP duplicates any TCP-listener services with an additional RUDP variant.
// The duplicate service will have name suffixed with "_rudp" and listener.type changed to "rudp".
// Other fields are kept identical.
func expandRUDP(services []map[string]any) []map[string]any {
    if len(services) == 0 { return services }
    out := make([]map[string]any, 0, len(services)*2)
    for _, s := range services {
        out = append(out, s)
        // inspect listener.type
        if lst, ok := s["listener"].(map[string]any); ok {
            if t, ok2 := lst["type"].(string); ok2 && t == "tcp" {
                // deep clone via JSON to keep nested structures
                b, err := json.Marshal(s)
                if err != nil { continue }
                var dup map[string]any
                if err := json.Unmarshal(b, &dup); err != nil { continue }
                // modify
                if name, _ := dup["name"].(string); name != "" {
                    dup["name"] = name + "_rudp"
                }
                if lst2, ok3 := dup["listener"].(map[string]any); ok3 {
                    lst2["type"] = "rudp"
                }
                out = append(out, dup)
            }
        }
    }
    return out
}

// expandNamesWithRUDP returns names plus their "_rudp" counterparts for bulk operations
// like DeleteService/PauseService/ResumeService.
func expandNamesWithRUDP(names []string) []string {
    if len(names) == 0 { return names }
    out := make([]string, 0, len(names)*2)
    for _, n := range names {
        out = append(out, n)
        if n != "" {
            out = append(out, n+"_rudp")
        }
    }
    return out
}

