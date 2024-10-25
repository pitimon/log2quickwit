/*
Program: eduroam-sp (Service Provider Accept Analysis)
Version: 2.2.2
Description: This program analyzes Access-Accept events for a specified service provider
             with focus on device usage patterns through station_id analysis.

Major changes in v2.2.2:
1. Added station_id based analysis and grouping
2. Refactored output structure to focus on device usage patterns
3. Modified output format to show authentication timestamps for each device
4. Changed output filename format to include -stationid suffix
5. Improved aggregation queries to handle device-centric analysis
6. Added summary statistics for unique devices and their usage 

Usage: ./eduroam-sp <service_provider> [days|Ny|yxxxx|DD-MM-YYYY]
      <service_provider>: The service provider to search for (e.g., 'eduroam.ku.ac.th')
      [days]: Optional. The number of days (1-3650) to look back from the current date.
      [Ny]: Optional. The number of years (1y-10y) to look back from the current date.
      [yxxxx]: Optional. Specific year (e.g., y2024)
      [DD-MM-YYYY]: Optional. A specific date to process data for.

Author: [P.Itarun]
Date: October 25, 2024
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
    "sync/atomic"
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
    Realm          string    `json:"realm"`
    ServiceProvider string    `json:"service_provider"`
    StationID      string    `json:"station_id"`
    Timestamp      time.Time `json:"timestamp"`
}

// UserActivity contains authentication activity for a user on a specific device
type UserActivity struct {
    Username       string
    Realm         string
    AuthTimestamps []time.Time
}

// StationStats contains statistics for a station_id
type StationStats struct {
    StationID    string
    TotalAuths   int
    Users        map[string]*UserActivity  // key: username
}

// StationStatsOutput ใช้สำหรับ JSON output
type StationStatsOutput struct {
    StationID           string         `json:"station_id"`
    TotalAuths          int            `json:"total_auths"`
    TotalUsers          int            `json:"total_users"`
    UsagePatterns       *UsagePattern  `json:"usage_patterns"`
    SessionAnalysis     *SessionAnalysis `json:"session_analysis"`
    PotentialIssues     []PotentialIssue `json:"potential_issues"`
    UserDetails         []UserDetail    `json:"user_details"`
}

// RealmStats contains statistics for a realm
type RealmStats struct {
    Realm         string
    Users         map[string]bool    // key: username
    Stations      map[string]bool    // key: station_id
    TotalAuths    int
}

// RealmStat for output
type RealmStat struct {
    Realm         string `json:"realm"`
    TotalUsers    int    `json:"total_users"`
    TotalStations int    `json:"total_stations"`
    TotalAuths    int    `json:"total_auths"`
}

// UserDetail for output
type UserDetail struct {
    Username       string    `json:"username"`
    Realm         string    `json:"realm"`
    AuthTimestamps []string `json:"auth_timestamps"`
}

// UsagePattern contains pattern analysis results
type UsagePattern struct {
    HourlyDistribution map[string]int    `json:"hourly_distribution"`
    AuthIntervals struct {
        AverageMinutes float64 `json:"average_minutes"`
        MinMinutes     int     `json:"min_minutes"`
        MaxMinutes     int     `json:"max_minutes"`
    } `json:"auth_intervals"`
    ActivePeriods []Period `json:"active_periods"`
    ConnectionStability struct {
        FrequentReauths []FrequentReauth `json:"frequent_reauths"`
        LongestGap      Period           `json:"longest_gap"`
    } `json:"connection_stability"`
}

// Period represents a time period
type Period struct {
    Start           string `json:"start"`
    End             string `json:"end"`
    DurationMinutes int    `json:"duration_minutes"`
    AuthCount       int    `json:"auth_count,omitempty"`
}

// FrequentReauth represents a period of frequent re-authentications
type FrequentReauth struct {
    Period   string `json:"period"`
    Count    int    `json:"count"`
    Interval string `json:"interval"`
}

// SessionAnalysis contains session-based analysis
type SessionAnalysis struct {
    TotalSessions           int      `json:"total_sessions"`
    AverageSessionDuration  string   `json:"average_session_duration"`
    SessionDetails         []Session `json:"session_details"`
}

// Session represents a single usage session
type Session struct {
    Start       string  `json:"start"`
    End         string  `json:"end"`
    Duration    string  `json:"duration"`
    AuthsCount  int     `json:"auths_count"`
    ReauthRate  string  `json:"reauth_rate"`
}

// PotentialIssue represents a potential connection issue
type PotentialIssue struct {
    Type        string `json:"type"`
    Period      string `json:"period"`
    Description string `json:"description"`
}

// Job represents a single day's query job
type Job struct {
    StartTimestamp int64
    EndTimestamp   int64
}


// SimplifiedOutputData represents the output JSON structure
type SimplifiedOutputData struct {
    QueryInfo struct {
        ServiceProvider string `json:"service_provider"`
        Days           int    `json:"days"`
        StartDate      string `json:"start_date"`
        EndDate        string `json:"end_date"`
    } `json:"query_info"`
    Summary struct {
        UniqueStations int `json:"unique_stations"`
        UniqueUsers    int `json:"unique_users"`
        UniqueRealms   int `json:"unique_realms"`
        TotalAuths     int `json:"total_authentications"`
    } `json:"summary"`
    StationStats []StationStatsOutput `json:"station_stats"`
    RealmStats   []RealmStat         `json:"realm_stats"`
}


// ฟังก์ชัน createOutputData ที่แก้ไขแล้ว
func createOutputData(result *Result, serviceProvider string, startDate, endDate time.Time, days int) SimplifiedOutputData {
    output := SimplifiedOutputData{}
    
    // Set query info
    output.QueryInfo.ServiceProvider = serviceProvider
    output.QueryInfo.Days = days
    output.QueryInfo.StartDate = startDate.Format("2006-01-02 15:04:05")
    output.QueryInfo.EndDate = endDate.Format("2006-01-02 15:04:05")

    // Calculate summary
    uniqueUsers := make(map[string]bool)
    totalAuths := 0
    for _, stats := range result.Stations {
        for username := range stats.Users {
            uniqueUsers[username] = true
        }
        totalAuths += stats.TotalAuths
    }

    output.Summary.UniqueStations = len(result.Stations)
    output.Summary.UniqueUsers = len(uniqueUsers)
    output.Summary.UniqueRealms = len(result.Realms)
    output.Summary.TotalAuths = totalAuths

    // Process station stats
    output.StationStats = make([]StationStatsOutput, 0, len(result.Stations))
    
    for stationID, stats := range result.Stations {
        stationStat := StationStatsOutput{
            StationID:  stationID,
            TotalAuths: stats.TotalAuths,
            TotalUsers: len(stats.Users),
            UserDetails: make([]UserDetail, 0, len(stats.Users)),
        }

        // Process each user's details
        for username, activity := range stats.Users {
            // Convert timestamps to RFC3339
            timestamps := make([]string, len(activity.AuthTimestamps))
            parsedTimestamps := make([]time.Time, len(activity.AuthTimestamps))
            
            for i, ts := range activity.AuthTimestamps {
                timestamps[i] = ts.Format(time.RFC3339)
                parsedTimestamps[i] = ts
            }

            userDetail := UserDetail{
                Username:       username,
                Realm:         activity.Realm,
                AuthTimestamps: timestamps,
            }
            stationStat.UserDetails = append(stationStat.UserDetails, userDetail)

            // Analyze patterns for this device
            usagePatterns := analyzeUsagePatterns(parsedTimestamps)
            if usagePatterns != nil {
                stationStat.UsagePatterns = usagePatterns
                stationStat.SessionAnalysis = analyzeSessionPatterns(parsedTimestamps)
                stationStat.PotentialIssues = analyzePotentialIssues(usagePatterns)
            }
        }

        // Sort UserDetails by username
        sort.Slice(stationStat.UserDetails, func(i, j int) bool {
            return stationStat.UserDetails[i].Username < stationStat.UserDetails[j].Username
        })

        output.StationStats = append(output.StationStats, stationStat)
    }

    // Sort StationStats by total_auths (descending)
    sort.Slice(output.StationStats, func(i, j int) bool {
        return output.StationStats[i].TotalAuths > output.StationStats[j].TotalAuths
    })

    // Process realm stats
    output.RealmStats = make([]RealmStat, 0, len(result.Realms))
    for realm, stats := range result.Realms {
        realmStat := RealmStat{
            Realm:         realm,
            TotalUsers:    len(stats.Users),
            TotalStations: len(stats.Stations),
            TotalAuths:    stats.TotalAuths,
        }
        output.RealmStats = append(output.RealmStats, realmStat)
    }

    // Sort RealmStats by total_auths (descending)
    sort.Slice(output.RealmStats, func(i, j int) bool {
        return output.RealmStats[i].TotalAuths > output.RealmStats[j].TotalAuths
    })

    return output
}

// แก้ไข struct กลางที่ใช้ในการประมวลผล
type Result struct {
    Stations    map[string]*StationStats  // key: station_id
    Realms      map[string]*RealmStats    // key: realm
}

// analyzeUsagePatterns วิเคราะห์ pattern การใช้งานจาก timestamps
func analyzeUsagePatterns(timestamps []time.Time) *UsagePattern {
    if len(timestamps) == 0 {
        return nil
    }

    // เรียงลำดับเวลา
    sort.Slice(timestamps, func(i, j int) bool {
        return timestamps[i].Before(timestamps[j])
    })

    pattern := &UsagePattern{
        HourlyDistribution: make(map[string]int),
    }

    // วิเคราะห์การกระจายตัวรายชั่วโมง
    for _, ts := range timestamps {
        hour := ts.Format("15:00-15:59")
        pattern.HourlyDistribution[hour]++
    }

    // วิเคราะห์ช่วงเวลาระหว่าง auth
    intervals := make([]float64, 0)
    for i := 1; i < len(timestamps); i++ {
        interval := timestamps[i].Sub(timestamps[i-1]).Minutes()
        intervals = append(intervals, interval)
    }

    if len(intervals) > 0 {
        // คำนวณค่าสถิติของ intervals
        var sum float64
        minInterval := intervals[0]
        maxInterval := intervals[0]
        for _, interval := range intervals {
            sum += interval
            if interval < minInterval {
                minInterval = interval
            }
            if interval > maxInterval {
                maxInterval = interval
            }
        }

        pattern.AuthIntervals.AverageMinutes = sum / float64(len(intervals))
        pattern.AuthIntervals.MinMinutes = int(minInterval)
        pattern.AuthIntervals.MaxMinutes = int(maxInterval)
    }

    // วิเคราะห์ช่วงที่มีการใช้งานต่อเนื่อง
    pattern.ActivePeriods = findActivePeriods(timestamps)

    // วิเคราะห์เสถียรภาพการเชื่อมต่อ
    pattern.ConnectionStability.FrequentReauths = findFrequentReauths(timestamps)
    pattern.ConnectionStability.LongestGap = findLongestGap(timestamps)

    return pattern
}

// sendQuickwitRequest handles HTTP communication with Quickwit
func sendQuickwitRequest(query map[string]interface{}, props Properties) (map[string]interface{}, error) {
    jsonQuery, err := json.Marshal(query)
    if err != nil {
        return nil, fmt.Errorf("error marshaling query: %v", err)
    }

    req, err := http.NewRequest("POST", props.QWURL+"/api/v1/nro-logs/search", strings.NewReader(string(jsonQuery)))
    if err != nil {
        return nil, fmt.Errorf("error creating request: %v", err)
    }

    req.SetBasicAuth(props.QWUser, props.QWPass)
    req.Header.Set("Content-Type", "application/json")
    req.Header.Set("Accept", "application/json")

    client := &http.Client{Timeout: 30 * time.Second}
    resp, err := client.Do(req)
    if err != nil {
        return nil, fmt.Errorf("error sending request: %v", err)
    }
    defer resp.Body.Close()

    body, err := io.ReadAll(resp.Body)
    if err != nil {
        return nil, fmt.Errorf("error reading response: %v", err)
    }

    if resp.StatusCode != http.StatusOK {
        return nil, fmt.Errorf("quickwit error (status %d): %s", resp.StatusCode, string(body))
    }

    var result map[string]interface{}
    if err := json.Unmarshal(body, &result); err != nil {
        return nil, fmt.Errorf("error decoding response: %v", err)
    }

    return result, nil
}

// processStationBucket processes a single station bucket
func processStationBucket(bucket map[string]interface{}, stationID string, resultChan chan<- LogEntry) {
    byUser, ok := bucket["by_user"].(map[string]interface{})
    if !ok {
        return
    }

    userBuckets, ok := byUser["buckets"].([]interface{})
    if !ok {
        return
    }

    for _, userBucketInterface := range userBuckets {
        userBucket, ok := userBucketInterface.(map[string]interface{})
        if !ok {
            continue
        }

        username := userBucket["key"].(string)

        // Process realm information
        if byRealm, ok := userBucket["by_realm"].(map[string]interface{}); ok {
            if realmBuckets, ok := byRealm["buckets"].([]interface{}); ok {
                if len(realmBuckets) > 0 {
                    if realmBucket, ok := realmBuckets[0].(map[string]interface{}); ok {
                        realm := realmBucket["key"].(string)
                        processUserAuthTimes(userBucket, username, realm, stationID, resultChan)
                    }
                }
            }
        }
    }
}

// processUserAuthTimes processes authentication timestamps for a user
func processUserAuthTimes(bucket map[string]interface{}, username, realm, stationID string, resultChan chan<- LogEntry) {
    authTimes, ok := bucket["auth_times"].(map[string]interface{})
    if !ok {
        return
    }

    timeBuckets, ok := authTimes["buckets"].([]interface{})
    if !ok {
        return
    }

    for _, timeBucketInterface := range timeBuckets {
        timeBucket, ok := timeBucketInterface.(map[string]interface{})
        if !ok || timeBucket["doc_count"].(float64) == 0 {
            continue
        }

        timestamp := time.Unix(int64(timeBucket["key"].(float64)/1000), 0)
        resultChan <- LogEntry{
            Username:        username,  // แน่ใจว่ามีการส่ง username
            Realm:          realm,
            StationID:      stationID,
            Timestamp:      timestamp,
        }
    }
}

// readProperties reads authentication properties from a file
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
                value = strings.Trim(value, "\"")
                
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

// getDomain returns the full domain name
func getDomain(input string) string {
    switch input {
    case "etlr1":
        return "etlr1.eduroam.org"
    case "etlr2":
        return "etlr2.eduroam.org"
    }
    
    if strings.HasPrefix(input, "eduroam.") {
        return input
    }
    
    return fmt.Sprintf("eduroam.%s", input)
}

// isLeapYear checks if a year is a leap year
func isLeapYear(year int) bool {
    return year%4 == 0 && (year%100 != 0 || year%400 == 0)
}

// analyzeSessionPatterns วิเคราะห์ session การใช้งาน
func analyzeSessionPatterns(timestamps []time.Time) *SessionAnalysis {
    if len(timestamps) < 2 {
        return nil
    }

    const sessionTimeout = 15 // นาทีที่ถือว่าเป็นคนละ session
    var analysis SessionAnalysis
    var currentSession Session
    var sessions []Session

    currentSession.Start = timestamps[0].Format(time.RFC3339)
    authCount := 1

    for i := 1; i < len(timestamps); i++ {
        gap := timestamps[i].Sub(timestamps[i-1]).Minutes()

        if gap > sessionTimeout {
            // จบ session เก่า
            currentSession.End = timestamps[i-1].Format(time.RFC3339)
            startTime, _ := time.Parse(time.RFC3339, currentSession.Start)
            endTime, _ := time.Parse(time.RFC3339, currentSession.End)
            duration := endTime.Sub(startTime).Minutes()
            currentSession.Duration = fmt.Sprintf("%.0f minutes", duration)
            currentSession.AuthsCount = authCount
            currentSession.ReauthRate = fmt.Sprintf("1 auth/%.1f minutes", duration/float64(authCount))
            sessions = append(sessions, currentSession)

            // เริ่ม session ใหม่
            currentSession = Session{
                Start: timestamps[i].Format(time.RFC3339),
            }
            authCount = 1
        } else {
            authCount++
        }
    }

    // จบ session สุดท้าย
    currentSession.End = timestamps[len(timestamps)-1].Format(time.RFC3339)
    startTime, _ := time.Parse(time.RFC3339, currentSession.Start)
    endTime, _ := time.Parse(time.RFC3339, currentSession.End)
    duration := endTime.Sub(startTime).Minutes()
    currentSession.Duration = fmt.Sprintf("%.0f minutes", duration)
    currentSession.AuthsCount = authCount
    currentSession.ReauthRate = fmt.Sprintf("1 auth/%.1f minutes", duration/float64(authCount))
    sessions = append(sessions, currentSession)

    // คำนวณค่าเฉลี่ย
    var totalDuration float64
    for _, session := range sessions {
        startTime, _ := time.Parse(time.RFC3339, session.Start)
        endTime, _ := time.Parse(time.RFC3339, session.End)
        totalDuration += endTime.Sub(startTime).Minutes()
    }

    analysis.TotalSessions = len(sessions)
    analysis.AverageSessionDuration = fmt.Sprintf("%.0f minutes", totalDuration/float64(len(sessions)))
    analysis.SessionDetails = sessions

    return &analysis
}

// findActivePeriods หาช่วงเวลาที่มีการใช้งานต่อเนื่อง
func findActivePeriods(timestamps []time.Time) []Period {
    if len(timestamps) < 2 {
        return nil
    }

    const maxGapMinutes = 15 // ช่วงห่างมากกว่า 15 นาทีถือเป็นคนละ period
    var periods []Period
    var currentPeriod Period
    currentPeriod.Start = timestamps[0].Format(time.RFC3339)
    authCount := 1

    for i := 1; i < len(timestamps); i++ {
        gap := timestamps[i].Sub(timestamps[i-1]).Minutes()
        
        if gap > maxGapMinutes {
            // จบ period เก่า
            currentPeriod.End = timestamps[i-1].Format(time.RFC3339)
            currentPeriod.AuthCount = authCount
            startTime, _ := time.Parse(time.RFC3339, currentPeriod.Start)
            endTime, _ := time.Parse(time.RFC3339, currentPeriod.End)
            currentPeriod.DurationMinutes = int(endTime.Sub(startTime).Minutes())
            periods = append(periods, currentPeriod)

            // เริ่ม period ใหม่
            currentPeriod = Period{
                Start: timestamps[i].Format(time.RFC3339),
            }
            authCount = 1
        } else {
            authCount++
        }
    }

    // จบ period สุดท้าย
    currentPeriod.End = timestamps[len(timestamps)-1].Format(time.RFC3339)
    currentPeriod.AuthCount = authCount
    startTime, _ := time.Parse(time.RFC3339, currentPeriod.Start)
    endTime, _ := time.Parse(time.RFC3339, currentPeriod.End)
    currentPeriod.DurationMinutes = int(endTime.Sub(startTime).Minutes())
    periods = append(periods, currentPeriod)

    return periods
}

// findFrequentReauths หาช่วงที่มีการ re-auth ถี่ผิดปกติ
func findFrequentReauths(timestamps []time.Time) []FrequentReauth {
    const maxInterval = 2 // นาทีระหว่าง auth
    const minCount = 3    // จำนวน auth ขั้นต่ำที่ถือว่าผิดปกติ
    
    var reauths []FrequentReauth
    var currentGroup []time.Time

    for i := 0; i < len(timestamps)-1; i++ {
        if len(currentGroup) == 0 {
            currentGroup = append(currentGroup, timestamps[i])
        }

        if timestamps[i+1].Sub(timestamps[i]).Minutes() <= maxInterval {
            currentGroup = append(currentGroup, timestamps[i+1])
        } else {
            if len(currentGroup) >= minCount {
                reauth := FrequentReauth{
                    Period: fmt.Sprintf("%s-%s",
                        currentGroup[0].Format("15:04"),
                        currentGroup[len(currentGroup)-1].Format("15:04")),
                    Count:    len(currentGroup),
                    Interval: fmt.Sprintf("%dmin", maxInterval),
                }
                reauths = append(reauths, reauth)
            }
            currentGroup = nil
        }
    }

    // ตรวจสอบกลุ่มสุดท้าย
    if len(currentGroup) >= minCount {
        reauth := FrequentReauth{
            Period: fmt.Sprintf("%s-%s",
                currentGroup[0].Format("15:04"),
                currentGroup[len(currentGroup)-1].Format("15:04")),
            Count:    len(currentGroup),
            Interval: fmt.Sprintf("%dmin", maxInterval),
        }
        reauths = append(reauths, reauth)
    }

    return reauths
}

// findLongestGap หาช่วงที่ไม่มีการใช้งานนานที่สุด
func findLongestGap(timestamps []time.Time) Period {
    var longestGap Period
    maxGap := 0.0

    for i := 1; i < len(timestamps); i++ {
        gap := timestamps[i].Sub(timestamps[i-1]).Minutes()
        if gap > maxGap {
            maxGap = gap
            longestGap = Period{
                Start:           timestamps[i-1].Format(time.RFC3339),
                End:             timestamps[i].Format(time.RFC3339),
                DurationMinutes: int(gap),
            }
        }
    }

    return longestGap
}

// processResults ปรับให้สอดคล้องกับ struct ที่แก้ไขแล้ว
func processResults(resultChan <-chan LogEntry, result *Result, mu *sync.Mutex) {
    for entry := range resultChan {
        mu.Lock()
        
        // Process station stats
        if _, exists := result.Stations[entry.StationID]; !exists {
            result.Stations[entry.StationID] = &StationStats{
                StationID: entry.StationID,
                Users:    make(map[string]*UserActivity),
            }
        }
        
        station := result.Stations[entry.StationID]
        if _, exists := station.Users[entry.Username]; !exists {
            station.Users[entry.Username] = &UserActivity{
                Username:       entry.Username,
                Realm:         entry.Realm,
                AuthTimestamps: []time.Time{},
            }
        }
        station.Users[entry.Username].AuthTimestamps = append(
            station.Users[entry.Username].AuthTimestamps,
            entry.Timestamp,
        )
        station.TotalAuths++

        // Process realm stats
        if _, exists := result.Realms[entry.Realm]; !exists {
            result.Realms[entry.Realm] = &RealmStats{
                Realm:    entry.Realm,
                Users:    make(map[string]bool),
                Stations: make(map[string]bool),
            }
        }
        
        realm := result.Realms[entry.Realm]
        realm.Users[entry.Username] = true
        realm.Stations[entry.StationID] = true
        realm.TotalAuths++

        mu.Unlock()
    }

    // เรียงลำดับ timestamps สำหรับทุก user activity
    mu.Lock()
    for _, station := range result.Stations {
        for _, activity := range station.Users {
            sort.Slice(activity.AuthTimestamps, func(i, j int) bool {
                return activity.AuthTimestamps[i].Before(activity.AuthTimestamps[j])
            })
        }
    }
    mu.Unlock()
}

// แก้ไขฟังก์ชัน worker เพื่อจำกัดจำนวน buckets
func worker(job Job, resultChan chan<- LogEntry, query map[string]interface{}, props Properties) (int64, error) {
    currentQuery := map[string]interface{}{
        "query": query["query"],
        "start_timestamp": job.StartTimestamp,
        "end_timestamp": job.EndTimestamp,
        "max_hits": 0,
        "aggs": map[string]interface{}{
            "by_station": map[string]interface{}{
                "terms": map[string]interface{}{
                    "field": "station_id",
                    "size": 1000,  // ลดจาก 10000
                },
                "aggs": map[string]interface{}{
                    "by_user": map[string]interface{}{
                        "terms": map[string]interface{}{
                            "field": "username",
                            "size": 100,   // ลดจาก 1000
                        },
                        "aggs": map[string]interface{}{
                            "by_realm": map[string]interface{}{
                                "terms": map[string]interface{}{
                                    "field": "realm",
                                    "size": 10,
                                },
                            },
                            "auth_times": map[string]interface{}{
                                "date_histogram": map[string]interface{}{
                                    "field": "timestamp",
                                    "fixed_interval": "1m",  // เปลี่ยนจาก 1s เป็น 1m
                                },
                            },
                        },
                    },
                },
            },
        },
    }

    result, err := sendQuickwitRequest(currentQuery, props)
    if err != nil {
        return 0, err
    }

    return processAggregations(result, resultChan)
}

// processAggregations processes the aggregation results
func processAggregations(result map[string]interface{}, resultChan chan<- LogEntry) (int64, error) {
    aggs, ok := result["aggregations"].(map[string]interface{})
    if !ok {
        return 0, fmt.Errorf("no aggregations in response")
    }

    byStation, ok := aggs["by_station"].(map[string]interface{})
    if !ok {
        return 0, fmt.Errorf("no by_station aggregation")
    }

    buckets, ok := byStation["buckets"].([]interface{})
    if !ok {
        return 0, fmt.Errorf("no buckets in by_station aggregation")
    }

    var totalHits int64
    for _, bucketInterface := range buckets {
        bucket, ok := bucketInterface.(map[string]interface{})
        if !ok {
            continue
        }

        stationID := bucket["key"].(string)
        docCount := int64(bucket["doc_count"].(float64))
        totalHits += docCount

        processStationBucket(bucket, stationID, resultChan)
    }

    return totalHits, nil
}

// analyzePotentialIssues วิเคราะห์ปัญหาที่อาจเกิดขึ้น
func analyzePotentialIssues(patterns *UsagePattern) []PotentialIssue {
    var issues []PotentialIssue

    // ตรวจสอบ frequent reauths
    for _, reauth := range patterns.ConnectionStability.FrequentReauths {
        issues = append(issues, PotentialIssue{
            Type:        "frequent_reauth",
            Period:      reauth.Period,
            Description: fmt.Sprintf("%d re-authentications within %s", reauth.Count, reauth.Interval),
        })
    }

    // ตรวจสอบ long gaps
    if patterns.ConnectionStability.LongestGap.DurationMinutes > 60 {
        issues = append(issues, PotentialIssue{
            Type:        "long_gap",
            Period:      fmt.Sprintf("%s to %s", 
                        patterns.ConnectionStability.LongestGap.Start,
                        patterns.ConnectionStability.LongestGap.End),
            Description: fmt.Sprintf("No activity for %d minutes", 
                        patterns.ConnectionStability.LongestGap.DurationMinutes),
        })
    }

    // ตรวจสอบ auth intervals ที่ผิดปกติ
    if patterns.AuthIntervals.MinMinutes < 1 {
        issues = append(issues, PotentialIssue{
            Type:        "rapid_reauth",
            Period:      "throughout session",
            Description: fmt.Sprintf("Some re-authentications occurred less than 1 minute apart"),
        })
    }

    return issues
}

func main() {
    if len(os.Args) < 2 || len(os.Args) > 3 {
        fmt.Println("Usage: ./eduroam-sp <service_provider> [days|Ny|yxxxx|DD-MM-YYYY]")
        fmt.Println("  service_provider: domain name (e.g., 'ku.ac.th', 'etlr1')")
        fmt.Println("  days: number of days (1-3650)")
        fmt.Println("  Ny: number of years (1y-10y)")
        fmt.Println("  yxxxx: specific year (e.g., y2024)")
        fmt.Println("  DD-MM-YYYY: specific date")
        os.Exit(1)
    }

    var serviceProvider string
    var startDate, endDate time.Time
    var days int
    var specificDate bool

    serviceProvider = getDomain(os.Args[1])

    if len(os.Args) == 3 {
        param := os.Args[2]
        
        if strings.HasPrefix(param, "y") && len(param) == 5 {
            yearStr := param[1:]
            if year, err := strconv.Atoi(yearStr); err == nil {
                if year >= 2000 && year <= 2100 {
                    startDate = time.Date(year, 1, 1, 0, 0, 0, 0, time.Local)
                    endDate = time.Date(year, 12, 31, 23, 59, 59, 999999999, time.Local)
                    days = 365
                    if isLeapYear(year) {
                        days = 366
                    }
                } else {
                    log.Fatalf("Invalid year range. Must be between 2000 and 2100")
                }
            } else {
                log.Fatalf("Invalid year format. Use y followed by 4 digits (e.g., y2024)")
            }
        } else if strings.HasSuffix(param, "y") {
            yearStr := strings.TrimSuffix(param, "y")
            if years, err := strconv.Atoi(yearStr); err == nil {
                if years >= 1 && years <= 10 {
                    days = years * 365
                    endDate = time.Now()
                    startDate = endDate.AddDate(0, 0, -days+1)
                } else {
                    log.Fatalf("Invalid year range. Must be between 1y and 10y")
                }
            } else {
                log.Fatalf("Invalid year format. Use 1y-10y")
            }
        } else if d, err := strconv.Atoi(param); err == nil {
            if d >= 1 && d <= 3650 {
                days = d
                endDate = time.Now()
                startDate = endDate.AddDate(0, 0, -days+1)
            } else {
                log.Fatalf("Invalid number of days. Must be between 1 and 3650")
            }
        } else {
            specificDate = true
            var err error
            startDate, err = time.Parse("02-01-2006", param)
            if err != nil {
                log.Fatalf("Invalid date format. Use DD-MM-YYYY: %v", err)
            }
            endDate = startDate.AddDate(0, 0, 1)
            days = 1
        }
    } else {
        days = 1
        endDate = time.Now()
        startDate = endDate.AddDate(0, 0, -1)
    }

    startDate = time.Date(startDate.Year(), startDate.Month(), startDate.Day(), 0, 0, 0, 0, startDate.Location())
    endDate = time.Date(endDate.Year(), endDate.Month(), endDate.Day(), 23, 59, 59, 999999999, endDate.Location())

    props, err := readProperties("qw-auth.properties")
    if err != nil {
        log.Fatalf("Error reading properties: %v", err)
    }

    if specificDate {
        fmt.Printf("Searching for date: %s\n", startDate.Format("2006-01-02"))
    } else {
        fmt.Printf("Searching from %s to %s\n", startDate.Format("2006-01-02"), endDate.Format("2006-01-02"))
    }

    query := map[string]interface{}{
        "query":           fmt.Sprintf(`message_type:"Access-Accept" AND service_provider:"%s"`, serviceProvider),
        "start_timestamp": startDate.Unix(),
        "end_timestamp":   endDate.Unix(),
        "max_hits":        10000,
    }

    resultChan := make(chan LogEntry, 10000)
    errChan := make(chan error, 1)
    var totalHits atomic.Int64
    var mu sync.Mutex
    var wg sync.WaitGroup

    jobs := make(chan Job, days)
    numWorkers := 10

    var processedDays int32
    queryStart := time.Now()

    result := &Result{
        Stations: make(map[string]*StationStats),
        Realms:   make(map[string]*RealmStats),
    }

    // Start worker pool
    for w := 1; w <= numWorkers; w++ {
        wg.Add(1)
        go func() {
            defer wg.Done()
            for job := range jobs {
                hits, err := worker(job, resultChan, query, props)
                if err != nil {
                    select {
                    case errChan <- err:
                    default:
                    }
                    return
                }
                totalHits.Add(hits)
                current := atomic.AddInt32(&processedDays, 1)
                fmt.Printf("\rProgress: %d/%d days processed, Progress hits: %d", 
                    current, days, totalHits.Load())
            }
        }()
    }

    processDone := make(chan struct{})
    go func() {
        processResults(resultChan, result, &mu)
        close(processDone)
    }()

    currentDate := startDate
    for currentDate.Before(endDate) {
        nextDate := currentDate.Add(24 * time.Hour)
        if nextDate.After(endDate) {
            nextDate = endDate
        }
        jobs <- Job{
            StartTimestamp: currentDate.Unix(),
            EndTimestamp:   nextDate.Unix(),
        }
        currentDate = nextDate
    }
    close(jobs)

    wg.Wait()
    close(resultChan)

    <-processDone

    select {
    case err := <-errChan:
        if err != nil {
            log.Fatalf("Error occurred: %v", err)
        }
    default:
    }

    queryDuration := time.Since(queryStart)

    fmt.Printf("\n")
    fmt.Printf("Number of unique stations: %d\n", len(result.Stations))
    fmt.Printf("Number of realms: %d\n", len(result.Realms))

    processStart := time.Now()
    outputData := createOutputData(result, serviceProvider, startDate, endDate, days)
    processDuration := time.Since(processStart)

    outputDir := fmt.Sprintf("output/%s", strings.Replace(serviceProvider, ".", "-", -1))
    if err := os.MkdirAll(outputDir, 0755); err != nil {
        log.Fatalf("Error creating output directory: %v", err)
    }

    currentTime := time.Now().Format("20060102-150405")
    var filename string
    if specificDate {
        filename = fmt.Sprintf("%s/%s-%s-stationid.json", outputDir, currentTime, startDate.Format("20060102"))
    } else if len(os.Args) > 2 && strings.HasPrefix(os.Args[2], "y") && len(os.Args[2]) == 5 {
        year := os.Args[2][1:]
        filename = fmt.Sprintf("%s/%s-%s-stationid.json", outputDir, currentTime, year)
    } else {
        filename = fmt.Sprintf("%s/%s-%dd-stationid.json", outputDir, currentTime, days)
    }

    jsonData, err := json.MarshalIndent(outputData, "", "  ")
    if err != nil {
        log.Fatalf("Error marshaling JSON: %v", err)
    }

    if err := os.WriteFile(filename, jsonData, 0644); err != nil {
        log.Fatalf("Error writing file: %v", err)
    }

    fmt.Printf("Results have been saved to %s\n", filename)
    fmt.Printf("Time taken:\n")
    fmt.Printf("  Quickwit query: %v\n", queryDuration)
    fmt.Printf("  Local processing: %v\n", processDuration)
    fmt.Printf("  Overall: %v\n", time.Since(queryStart))
}
