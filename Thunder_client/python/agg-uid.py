#!/usr/bin/env python3

import requests
from requests.auth import HTTPBasicAuth
from collections import defaultdict
import sys
import datetime
import re
import os
import json
import time

def read_properties(file_path):
    properties = {}
    with open(file_path, 'r') as f:
        for line in f:
            line = line.strip()
            if line and not line.startswith('#'):
                key, value = line.split('=', 1)
                properties[key.strip()] = value.strip()
    return properties['QW_USER'], properties['QW_PASS'], properties['QW_URL'].lstrip('=')


def get_quickwit_results(query, auth, url):
    headers = {
        "Content-Type": "application/json",
        "Accept": "application/json"
    }
    
    response = requests.post(f"{url}/api/v1/nro-logs/search", json=query, auth=auth, headers=headers)
    response.raise_for_status()
    return response.json()

def process_results(aggregations, domain):
    user_counts = defaultdict(int)
    pattern = re.compile(f"Access-Reject for user ([^@]+@{domain}\\.ac\\.th)")
    
    for bucket in aggregations['unique_users']['buckets']:
        match = pattern.search(bucket['key'])
        if match:
            user = match.group(1)
            user_counts[user] += bucket['doc_count']
    
    return dict(user_counts)

def get_timestamp_range(days):
    end_timestamp = int(time.time())
    start_timestamp = end_timestamp - (days * 24 * 60 * 60)
    return start_timestamp, end_timestamp

def timestamp_to_human_readable(timestamp):
    return datetime.datetime.fromtimestamp(timestamp).strftime('%Y-%m-%d %H:%M:%S')

def main(domain, days):
    qw_user, qw_pass, qw_url = read_properties('qw-auth.properties')
    auth = HTTPBasicAuth(qw_user, qw_pass)

    start_timestamp, end_timestamp = get_timestamp_range(days)

    query = {
        "query": f"full_message:\"Access-Reject for user\" AND full_message:\"@{domain}\" AND full_message:\"from eduroam.{domain}\"",
        "start_timestamp": start_timestamp,
        "end_timestamp": end_timestamp,
        "max_hits": 0,
        "aggs": {
            "unique_users": {
                "terms": {
                    "field": "full_message",
                    "size": 65000
                }
            }
        }
    }

    try:
        quickwit_response = get_quickwit_results(query, auth, qw_url)
        results = process_results(quickwit_response['aggregations'], domain)

        if not os.path.exists(domain):
            os.makedirs(domain)

        current_time = datetime.datetime.now().strftime("%Y%m%d-%H%M%S")
        filename = f"{domain}/{current_time}.json"

        sorted_results = [{"user": user, "count": count} for user, count in sorted(results.items(), key=lambda x: x[1], reverse=True)]

        with open(filename, 'w') as f:
            json.dump({
                "start_timestamp": start_timestamp,
                "end_timestamp": end_timestamp,
                "start_time": timestamp_to_human_readable(start_timestamp),
                "end_time": timestamp_to_human_readable(end_timestamp),
                "results": sorted_results
            }, f, indent=2)
        
        print(f"Results have been saved to {filename}")

    except requests.exceptions.RequestException as e:
        print(f"An error occurred: {e}")

if __name__ == "__main__":
    if len(sys.argv) < 2 or len(sys.argv) > 3:
        print("Usage: python agg-uid.py <domain> [days]")
        sys.exit(1)
    
    domain = sys.argv[1]
    days = int(sys.argv[2]) if len(sys.argv) == 3 else 1
    main(domain, days)