#!/usr/bin/env python3

from datetime import datetime, timedelta, timezone
import sys

def generate_timestamp_range(days):
    end_time = datetime.now(timezone.utc)
    start_time = end_time - timedelta(days=days)
    
    start_iso = start_time.strftime("%Y-%m-%dT%H:%M:%SZ")
    end_iso = end_time.strftime("%Y-%m-%dT%H:%M:%SZ")
    
    return f"timestamp:[{start_iso} TO {end_iso}]"

if __name__ == "__main__":
    if len(sys.argv) != 2:
        print("Usage: python script.py <number_of_days>")
        sys.exit(1)
    
    try:
        days = int(sys.argv[1])
        if days <= 0:
            raise ValueError("Number of days must be positive")
    except ValueError as e:
        print(f"Error: {e}")
        sys.exit(1)
    
    timestamp_range = generate_timestamp_range(days)
    print(timestamp_range)