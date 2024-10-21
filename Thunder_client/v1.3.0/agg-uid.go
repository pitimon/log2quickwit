/*
Program: agg-uid (Aggregate User IDs)
Version: 1.3.0
Description: This program aggregates Access-Reject events for users from a specified domain
             using the Quickwit search engine. It collects data over a specified time range,
             processes the results, and outputs the aggregated data to a JSON file.

Usage: ./agg-uid <domain> [days]
  <domain>: The domain to search for (e.g., 'example.ac.th')
  [days]: Optional. The number of days to look back from the current date. Default is 1.

Features:
- Concurrent querying using goroutines for improved performance
- Flexible time range specification
- Aggregation of user access reject events
- Output of results in JSON format with timing information

Changes in version 1.3.0:
- Added timing information for Quickwit queries, local processing, and overall execution
- Improved error handling and logging
- Adjusted output file naming to include the number of days
- Implemented goroutines for concurrent Quickwit querying

Author: [P.Itarun]
Date: [19 Oct 2024]
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
    "regexp"
    "sort"
    "strconv"
    "strings"
    "time"
    "sync"
)

type Properties struct {
    QWUser string
    QWPass string
    QWURL  string
}

type Result struct {
    User  string `json:"user"`
    Count int    `json:"count"`
}

type OutputData struct {
    Description     string   `json:"description"`
    QuerySummary    string   `json:"query_summary"`
    AggregationLogic string   `json:"aggregation_logic"`
    Note            string   `json:"note"`
    StartTimestamp  int64    `json:"start_timestamp"`
    EndTimestamp    int64    `json:"end_timestamp"`
    StartTime       string   `json:"start_time"`
    EndTime         string   `json:"end_time"`
    Results         []Result `json:"results"`
}

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
                    props.QWURL = strings.TrimPrefix(value, "=") // ตัดเครื่องหมาย = ออก
                }
            }
        }
    }
    return props, scanner.Err()
}

func getQuickwitResults(query map[string]interface{}, auth Properties) (map[string]interface{}, error) {
    client := &http.Client{}
    jsonQuery, _ := json.Marshal(query)
    req, err := http.NewRequest("POST", auth.QWURL+"/api/v1/nro-logs/search", strings.NewReader(string(jsonQuery)))
    if err != nil {
        return nil, err
    }

    req.SetBasicAuth(auth.QWUser, auth.QWPass)
    req.Header.Set("Content-Type", "application/json")
    req.Header.Set("Accept", "application/json")

    resp, err := client.Do(req)
    if err != nil {
        return nil, err
    }
    defer resp.Body.Close()

    body, err := io.ReadAll(resp.Body) // เปลี่ยนจาก ioutil.ReadAll เป็น io.ReadAll
    if err != nil {
        return nil, err
    }

    var result map[string]interface{}
    err = json.Unmarshal(body, &result)
    return result, err
}

func processResults(aggregations map[string]interface{}, domain string) map[string]int {
    userCounts := make(map[string]int)
    pattern := regexp.MustCompile(fmt.Sprintf(`Access-Reject for user ([^@]+@%s\.ac\.th)`, domain))

    if buckets, ok := aggregations["unique_users"].(map[string]interface{})["buckets"].([]interface{}); ok {
        for _, bucket := range buckets {
            if b, ok := bucket.(map[string]interface{}); ok {
                if key, ok := b["key"].(string); ok {
                    matches := pattern.FindStringSubmatch(key)
                    if len(matches) > 1 {
                        user := matches[1]
                        count := int(b["doc_count"].(float64))
                        userCounts[user] += count
                    }
                }
            }
        }
    }
    return userCounts
}

func getTimestampRange(days int) (int64, int64) {
    endTimestamp := time.Now().Unix()
    startTimestamp := endTimestamp - int64(days*24*60*60)
    return startTimestamp, endTimestamp
}

func timestampToHumanReadable(timestamp int64) string {
    return time.Unix(timestamp, 0).Format("2006-01-02 15:04:05")
}

func getTimestampRanges(totalDays int) [][]int64 {
    endTimestamp := time.Now().Unix()
    startTimestamp := endTimestamp - int64(totalDays*24*60*60)
    var ranges [][]int64

    for start := startTimestamp; start < endTimestamp; start += 30 * 24 * 60 * 60 {
        end := start + 30*24*60*60
        if end > endTimestamp {
            end = endTimestamp
        }
        ranges = append(ranges, []int64{start, end})
    }

    return ranges
}

func main() {
    overallStart := time.Now()

    if len(os.Args) < 2 || len(os.Args) > 3 {
        fmt.Println("Usage: ./agg-uid <domain> [days]")
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

    timeRanges := getTimestampRanges(days)
    allResults := make(map[string]int)
    var mutex sync.Mutex
    var wg sync.WaitGroup

    semaphore := make(chan struct{}, 5) // Limit to 5 concurrent goroutines
    
    var quickwitTime time.Duration
    var quickwitMutex sync.Mutex

    for _, timeRange := range timeRanges {
        wg.Add(1)
        semaphore <- struct{}{}
        go func(tr []int64) {
            defer wg.Done()
            defer func() { <-semaphore }()
    
            queryStart := time.Now()
            query := map[string]interface{}{
                "query":           fmt.Sprintf(`full_message:"Access-Reject for user" AND full_message:"@%s" AND full_message:"from eduroam.%s"`, domain, domain),
                "start_timestamp": tr[0],
                "end_timestamp":   tr[1],
                "max_hits":        0,
                "aggs": map[string]interface{}{
                    "unique_users": map[string]interface{}{
                        "terms": map[string]interface{}{
                            "field": "full_message",
                            "size":  65000,
                        },
                    },
                },
            }
    
            quickwitResponse, err := getQuickwitResults(query, props)
            queryDuration := time.Since(queryStart)
    
            quickwitMutex.Lock()
            quickwitTime += queryDuration
            quickwitMutex.Unlock()
    
            if err != nil {
                log.Printf("Error getting Quickwit results for range %v: %v", tr, err)
                return
            }
    
            results := processResults(quickwitResponse["aggregations"].(map[string]interface{}), domain)
            
            mutex.Lock()
            for user, count := range results {
                allResults[user] += count
            }
            mutex.Unlock()
        }(timeRange)
    }

    wg.Wait()

    localProcessStart := time.Now()

    // Create output directory structure
    outputDir := fmt.Sprintf("output/%s", domain)
    if err := os.MkdirAll(outputDir, 0755); err != nil {
        log.Fatalf("Error creating output directory: %v", err)
    }

    var sortedResults []Result
    for user, count := range allResults {
        sortedResults = append(sortedResults, Result{User: user, Count: count})
    }
    sort.Slice(sortedResults, func(i, j int) bool {
        return sortedResults[i].Count > sortedResults[j].Count
    })

    currentTime := time.Now().Format("20060102-150405")
    filename := fmt.Sprintf("%s/%s-%dd.json", outputDir, currentTime, days)

    startTimestamp := time.Now().Unix() - int64(days*24*60*60)
    endTimestamp := time.Now().Unix()

    description := "This file contains aggregated data of Access-Reject events for users from the specified domain."
    
    querySummary := fmt.Sprintf(`- Event Type: Access-Reject for user
- Domain: %s
- Source: from eduroam.%s
- Time Range: %s to %s
- Data Period: Last %d days from the query execution date`, 
        domain, domain, 
        timestampToHumanReadable(startTimestamp), 
        timestampToHumanReadable(endTimestamp),
        days)

    aggregationLogic := `1. Collected all "Access-Reject" events for users from the specified domain within the given time range.
2. Extracted unique usernames (in the format user@domain.ac.th) from the full message of each event.
3. Counted the occurrences of each unique username.
4. Sorted the results by count in descending order.
Note: Data was collected in 30-day intervals to ensure completeness and improve performance.`

    note := "This data represents authentication failures and may be useful for identifying potential issues with user accounts or analyzing patterns in failed login attempts."

    outputData := OutputData{
        Description:     description,
        QuerySummary:    querySummary,
        AggregationLogic: aggregationLogic,
        Note:            note,
        StartTimestamp:  startTimestamp,
        EndTimestamp:    endTimestamp,
        StartTime:       timestampToHumanReadable(startTimestamp),
        EndTime:         timestampToHumanReadable(endTimestamp),
        Results:         sortedResults,
    }

    jsonData, err := json.MarshalIndent(outputData, "", "  ")
    if err != nil {
        log.Fatalf("Error marshaling JSON: %v", err)
    }

    err = os.WriteFile(filename, jsonData, 0644)
    if err != nil {
        log.Fatalf("Error writing file: %v", err)
    }

    localProcessDuration := time.Since(localProcessStart)
    overallDuration := time.Since(overallStart)

    fmt.Printf("Results have been saved to %s\n", filename)
    fmt.Printf("Time taken:\n")
    fmt.Printf("  Quickwit queries (total across all goroutines): %v\n", quickwitTime)
    fmt.Printf("  Local processing: %v\n", localProcessDuration)
    fmt.Printf("  Overall: %v\n", overallDuration)
}
