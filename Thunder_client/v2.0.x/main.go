/*
Program: eduroam-accept (User Accept Roaming)
Version: 2.0.2
Description: This program aggregates Access-Accept events for users from a specified domain
             using the Quickwit search engine. It collects data over a specified time range,
             processes the results, and outputs the aggregated data to a JSON file.

Usage: ./eduroam-accept <domain> [days]
  <domain>: The domain to search for (e.g., 'example.ac.th' or 'etlr1' or 'etlr2')
  [days]: Optional. The number of days to look back from the current date. Default is 1.

Features:
- Concurrent querying and processing using goroutines for improved performance
- Flexible time range specification
- Aggregation of user access accept events
- Output of results in JSON format with timing information
- Simplified output structure for easier consumption

Changes in version 2.0.2:
- Changed output format to a simplified structure
- Improved comments and documentation
- Added support for 'etlr2' domain
- Modified getDomain function to handle different domain formats
- Added summary information in the output
- ใช้การ query แบบวันต่อวัน (incremental) เพื่อหลีกเลี่ยงข้อจำกัดของ max_hits และ start_offset

Changes in version 2.0.0:
- Changed query from "Access-Reject" to "Access-Accept"
- Updated result processing to count days of activity instead of event occurrences
- Added service provider information to the output
- Restructured output to show data by username and service provider 
- Improved error handling and logging

Author: [P.Itarun]
Date: [20 Oct 2024]
License: [License Information if applicable]
*/

package main

import (
    "bufio"
    "encoding/json"
    "fmt"
	"io"
    "log"
    "net/http"
    "os"
    "sort"
    "strconv"
    "strings"
    "sync"
    "time"
)

// Properties represents the authentication properties for Quickwit API
type Properties struct {
    QWUser string
    QWPass string
    QWURL  string
}

// LogEntry represents a single log entry from Quickwit search results
type LogEntry struct {
    Username        string    `json:"username"`
    ServiceProvider string    `json:"service_provider"`
    Timestamp       time.Time `json:"timestamp"`
}

// UserStats contains statistics for a user
type UserStats struct {
    DaysActive int
    Providers  map[string]bool
}

// ProviderStats contains statistics for a service provider
type ProviderStats struct {
    Users map[string]bool
}

// Result holds the aggregated results
type Result struct {
    Users     map[string]*UserStats
    Providers map[string]*ProviderStats
}

// OutputData represents the structure of the output JSON file
type OutputData struct {
    QueryInfo            QueryInfo           `json:"query_info"`
    Description          string              `json:"description"`
    QuerySummary         string              `json:"query_summary"`
    AggregationLogic     string              `json:"aggregation_logic"`
    Note                 string              `json:"note"`
    StartTimestamp       int64               `json:"start_timestamp"`
    EndTimestamp         int64               `json:"end_timestamp"`
    StartTime            string              `json:"start_time"`
    EndTime              string              `json:"end_time"`
    UsernameStats        []UserStatOutput    `json:"username_stats"`
    ServiceProviderStats []ProviderStatOutput `json:"service_provider_stats"`
}

// QueryInfo contains information about the Quickwit query
type QueryInfo struct {
    Domain    string `json:"domain"`
    Days      int    `json:"days"`
    StartDate string `json:"start_date"`
    EndDate   string `json:"end_date"`
}

// UserStatOutput represents a single user stat for output
type UserStatOutput struct {
    Username   string `json:"username"`
    DaysActive int    `json:"days_active"`
}

// ProviderStatOutput represents a single provider stat for output  
type ProviderStatOutput struct {
    ServiceProvider string          `json:"service_provider"`
    Users           []UserStatOutput `json:"users"`
}

// SimplifiedOutputData represents a simplified structure of the output JSON file
type SimplifiedOutputData struct {
    QueryInfo struct {
        Domain    string `json:"domain"`
        Days      int    `json:"days"`
        StartDate string `json:"start_date"`
        EndDate   string `json:"end_date"`
    } `json:"query_info"`
    Description   string `json:"description"`
    Summary       struct {
        TotalUsers     int `json:"total_users"`
        TotalProviders int `json:"total_providers"`
    } `json:"summary"`
    ProviderStats []struct {
        Provider string   `json:"provider"`
        UserCount int     `json:"user_count"`
        Users    []string `json:"users"`
    } `json:"provider_stats"`
    UserStats []struct {
        Username   string   `json:"username"`
        DaysActive int      `json:"days_active"`
        Providers  []string `json:"providers"`
    } `json:"user_stats"`
}

// createSimplifiedOutputData creates a simplified output data structure
func createSimplifiedOutputData(result *Result, domain string, days int, startTimestamp, endTimestamp int64) SimplifiedOutputData {
    output := SimplifiedOutputData{}
    
    output.QueryInfo.Domain = domain
    output.QueryInfo.Days = days
    output.QueryInfo.StartDate = time.Unix(startTimestamp, 0).Format("2006-01-02 15:04:05")
    output.QueryInfo.EndDate = time.Unix(endTimestamp, 0).Format("2006-01-02 15:04:05")
    
    output.Description = "Aggregated Access-Accept events for the specified domain and time range."

    // Add summary
    output.Summary.TotalUsers = len(result.Users)
    output.Summary.TotalProviders = len(result.Providers)

    // Process provider stats
    for provider, stats := range result.Providers {
        users := make([]string, 0, len(stats.Users))
        for user := range stats.Users {
            users = append(users, user)
        }
        output.ProviderStats = append(output.ProviderStats, struct {
            Provider string   `json:"provider"`
            UserCount int     `json:"user_count"`
            Users    []string `json:"users"`
        }{
            Provider: provider,
            UserCount: len(users),
            Users:    users,
        })
    }

    // Sort provider stats by number of users
    sort.Slice(output.ProviderStats, func(i, j int) bool {
        return output.ProviderStats[i].UserCount > output.ProviderStats[j].UserCount
    })

    // Process user stats
    for username, stats := range result.Users {
        providers := make([]string, 0, len(stats.Providers))
        for provider := range stats.Providers {
            providers = append(providers, provider)
        }
        output.UserStats = append(output.UserStats, struct {
            Username   string   `json:"username"`
            DaysActive int      `json:"days_active"`
            Providers  []string `json:"providers"`
        }{
            Username:   username,
            DaysActive: stats.DaysActive,
            Providers:  providers,
        })
    }

    // Sort user stats by days active
    sort.Slice(output.UserStats, func(i, j int) bool {
        return output.UserStats[i].DaysActive > output.UserStats[j].DaysActive
    })

    return output
}

// readProperties reads the authentication properties from a file
func readProperties(filePath string) (Properties, error) {
    file, err := os.Open(filePath)
    if err != nil {
        return Properties{}, err
    }
    defer file.Close()

    props := Properties{}
    scanner := bufio.NewScanner(file)
    for scanner.Scan() {
        line := scanner.Text()
        if line != "" && !strings.HasPrefix(line, "#") {
            parts := strings.SplitN(line, "=", 2)
            if len(parts) == 2 {
                key := strings.TrimSpace(parts[0])
                value := strings.TrimSpace(parts[1])
                switch key {
                case "QW_USER":
                    props.QWUser = value
                case "QW_PASS":
                    props.QWPass = value
                case "QW_URL":
                    props.QWURL = strings.TrimPrefix(value, "=")
                }
            }
        }
    }
    return props, scanner.Err()
}

// getQuickwitResults retrieves search results from Quickwit API
func getQuickwitResults(query map[string]interface{}, auth Properties, resultChan chan<- LogEntry, errChan chan<- error) error {
    client := &http.Client{}
    jsonQuery, _ := json.Marshal(query)
    req, err := http.NewRequest("POST", auth.QWURL+"/api/v1/nro-logs/search", strings.NewReader(string(jsonQuery)))
    if err != nil {
        return fmt.Errorf("error creating request: %v", err)
    }

    req.SetBasicAuth(auth.QWUser, auth.QWPass)
    req.Header.Set("Content-Type", "application/json")
    req.Header.Set("Accept", "application/json")

    resp, err := client.Do(req)
    if err != nil {
        return fmt.Errorf("error sending request: %v", err)
    }
    defer resp.Body.Close()

    bodyBytes, _ := io.ReadAll(resp.Body)
    
    var result map[string]interface{}
    if err := json.Unmarshal(bodyBytes, &result); err != nil {
        return fmt.Errorf("error decoding response: %v", err)
    }

    hits, ok := result["hits"].([]interface{})
    if !ok {
        return fmt.Errorf("unexpected response structure: hits not found or not an array")
    }

    for _, hitInterface := range hits {
        hit, ok := hitInterface.(map[string]interface{})
        if !ok {
            log.Printf("Skipping invalid hit structure")
            continue
        }

        entry := LogEntry{
            Username:        hit["username"].(string),
            ServiceProvider: hit["service_provider"].(string),
        }

        if timestampStr, ok := hit["timestamp"].(string); ok {
            if timestamp, err := time.Parse(time.RFC3339, timestampStr); err == nil {
                entry.Timestamp = timestamp
            } else {
                log.Printf("Error parsing timestamp: %v", err)
            }
        }

        resultChan <- entry
    }

    return nil
}


// processResults processes the search results and updates the result struct
func processResults(resultChan <-chan LogEntry, result *Result, mu *sync.Mutex, startDate, endDate time.Time) {
    localUserDays := make(map[string]map[string]bool)
    localUsers := make(map[string]*UserStats)
    localProviders := make(map[string]*ProviderStats)

    for entry := range resultChan {
        if entry.Timestamp.Before(startDate) || entry.Timestamp.After(endDate) {
            continue // Skip entries outside the specified date range
        }

        // Process user stats
        if _, exists := localUsers[entry.Username]; !exists {
            localUsers[entry.Username] = &UserStats{
                DaysActive: 0,
                Providers:  make(map[string]bool),
            }
            localUserDays[entry.Username] = make(map[string]bool)
        }
        day := entry.Timestamp.Format("2006-01-02")
        if !localUserDays[entry.Username][day] {
            localUserDays[entry.Username][day] = true
            localUsers[entry.Username].DaysActive++
        }
        localUsers[entry.Username].Providers[entry.ServiceProvider] = true

        // Process provider stats
        if _, exists := localProviders[entry.ServiceProvider]; !exists {
            localProviders[entry.ServiceProvider] = &ProviderStats{
                Users: make(map[string]bool),
            }
        }
        localProviders[entry.ServiceProvider].Users[entry.Username] = true
    }

    // Merge local results into global result
    mu.Lock()
    defer mu.Unlock()
    for username, stats := range localUsers {
        if _, exists := result.Users[username]; !exists {
            result.Users[username] = stats
        } else {
            result.Users[username].DaysActive = stats.DaysActive // Use the correct count
            for provider := range stats.Providers {
                result.Users[username].Providers[provider] = true
            }
        }
    }
    for provider, stats := range localProviders {
        if _, exists := result.Providers[provider]; !exists {
            result.Providers[provider] = stats
        } else {
            for user := range stats.Users {
                result.Providers[provider].Users[user] = true
            }
        }
    }
}

// getTimestampRange calculates the start and end timestamps based on the specified number of days
func getTimestampRange(days int) (int64, int64) {
    endTimestamp := time.Now().Unix()
    startTimestamp := endTimestamp - int64(days*24*60*60)
    return startTimestamp, endTimestamp
}

// timestampToHumanReadable converts a Unix timestamp to a human-readable string
//lint:ignore U1000 This function may be used in the future
func timestampToHumanReadable(timestamp int64) string {
    return time.Unix(timestamp, 0).Format("2006-01-02 15:04:05")
}

// getDomain returns the full domain name based on the input
func getDomain(input string) string {
    if input == "etlr1" {
        return "etlr1.eduroam.org"
    }
	if input == "etlr2" {
        return "etlr2.eduroam.org"
    }
    return fmt.Sprintf("eduroam.%s", input)
    // return fmt.Sprintf("eduroam.%s.ac.th", input)
}

func main() {
    // Set logging flags
    log.SetFlags(log.LstdFlags | log.Lshortfile)
    
    // Record overall start time 
    overallStart := time.Now()

    if len(os.Args) < 2 || len(os.Args) > 3 {
        fmt.Println("Usage: ./eduroam-accept <domain> [days]")
        os.Exit(1)
    }

    domain := os.Args[1]
    days := 1
    if len(os.Args) == 3 {
        var err error
        days, err = strconv.Atoi(os.Args[2])
        if err != nil {
            log.Fatalf("Invalid days parameter: %v", err)
        }
    }

    props, err := readProperties("qw-auth.properties")
    if err != nil {
        log.Fatalf("Error reading properties: %v", err)
    }

    startTimestamp, endTimestamp := getTimestampRange(days)
    startDate := time.Unix(startTimestamp, 0)
    endDate := time.Unix(endTimestamp, 0)
    
    log.Printf("Searching from %s to %s", startDate, endDate)

    query := map[string]interface{}{
        "query":           fmt.Sprintf(`message_type:"Access-Accept" AND realm:"%s" NOT service_provider:"client"`, getDomain(domain)),
        "start_timestamp": startTimestamp,
        "end_timestamp":   endTimestamp,
        "max_hits":        10000,
        "sort_by_field":   "_timestamp",
    }
    
    resultChan := make(chan LogEntry, 100)
    errChan := make(chan error, 1)

    result := &Result{
        Users:     make(map[string]*UserStats),
        Providers: make(map[string]*ProviderStats),
    }

    var mu sync.Mutex
    var wg sync.WaitGroup

    // Start worker goroutines
    numWorkers := 5
    for i := 0; i < numWorkers; i++ {
        wg.Add(1)
        go func() {
            defer wg.Done()
            processResults(resultChan, result, &mu, startDate, endDate)
        }()
    }

    // Start query goroutine
    queryStart := time.Now()
    go func() {
        defer close(resultChan)
        currentStartTimestamp := startTimestamp
        for currentStartTimestamp < endTimestamp {
            currentQuery := make(map[string]interface{})
            for k, v := range query {
                currentQuery[k] = v
            }
            currentQuery["start_timestamp"] = currentStartTimestamp
            currentEndTimestamp := currentStartTimestamp + 24*60*60 // 1 day
            if currentEndTimestamp > endTimestamp {
                currentEndTimestamp = endTimestamp
            }
            currentQuery["end_timestamp"] = currentEndTimestamp

            err := getQuickwitResults(currentQuery, props, resultChan, errChan)
            if err != nil {
                errChan <- err
                return
            }
            currentStartTimestamp = currentEndTimestamp
        }
    }()

    // Wait for worker goroutines to finish
    wg.Wait()

    // Check for errors
    select {
    case err := <-errChan:
        if err != nil {
            log.Printf("Error occurred: %v", err)
            return
        }
    default:
        // No error
    }

    queryDuration := time.Since(queryStart)

    log.Printf("Number of users: %d", len(result.Users))
    log.Printf("Number of providers: %d", len(result.Providers))

    // Start measuring local processing time
    processStart := time.Now()

    // Create simplified output data
    outputData := createSimplifiedOutputData(result, domain, days, startTimestamp, endTimestamp)

    processDuration := time.Since(processStart)

    outputDir := fmt.Sprintf("output/%s", domain)
    if err := os.MkdirAll(outputDir, 0755); err != nil {
        log.Fatalf("Error creating output directory: %v", err)
    }

    currentTime := time.Now().Format("20060102-150405")
    filename := fmt.Sprintf("%s/%s-%dd.json", outputDir, currentTime, days)

    jsonData, err := json.MarshalIndent(outputData, "", "  ")
    if err != nil {
        log.Fatalf("Error marshaling JSON: %v", err)
    }

    err = os.WriteFile(filename, jsonData, 0644)
    if err != nil {
        log.Fatalf("Error writing file: %v", err)
    }

    overallDuration := time.Since(overallStart)

    fmt.Printf("Results have been saved to %s\n", filename)
    fmt.Printf("Time taken:\n")
    fmt.Printf("  Quickwit query: %v\n", queryDuration)
    fmt.Printf("  Local processing: %v\n", processDuration)
    fmt.Printf("  Overall: %v\n", overallDuration)
}