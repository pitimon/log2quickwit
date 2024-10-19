```
{
  "version": "0.8",
  "index_id": "nro-logs",
  "doc_mapping": {
    "field_mappings": [
      {
        "name": "timestamp",
        "type": "datetime",
        "stored": true,
        "indexed": true,
        "fast": true,
        "input_formats": ["rfc3339", "%b %d %H:%M:%S", "%Y-%m-%d %H:%M:%S"],
        "output_format": "rfc3339"
      },
      {
        "name": "hostname",
        "type": "text",
        "stored": true,
        "indexed": true,
        "tokenizer": "raw",
        "fast": true
      },
      {
        "name": "process",
        "type": "text",
        "stored": true,
        "indexed": true,
        "tokenizer": "raw",
        "fast": true
      },
      {
        "name": "pid",
        "type": "i64",
        "stored": true
      },
      {
        "name": "message_type",
        "type": "text",
        "stored": true,
        "indexed": true,
        "tokenizer": "raw",
        "fast": true
      },
      {
        "name": "destination_ip",
        "type": "ip",
        "stored": true,
        "indexed": true,
        "fast": true
      },
      {
        "name": "username",
        "type": "text",
        "stored": true,
        "indexed": true,
        "tokenizer": "default",
        "record": "position",
        "fast": true
      },
      {
        "name": "station_id",
        "type": "text",
        "stored": true,
        "indexed": true,
        "tokenizer": "raw",
        "fast": true
      },
      {
        "name": "realm",
        "type": "text",
        "stored": true,
        "indexed": true,
        "tokenizer": "raw",
        "fast": true
      },
      {
        "name": "service_provider",
        "type": "text",
        "stored": true,
        "indexed": true,
        "tokenizer": "raw",
        "fast": true
      },
      {
        "name": "full_message",
        "type": "text",
        "stored": true,
        "indexed": true,
        "tokenizer": "default",
        "record": "position",
        "fast": true
      }
    ],
    "timestamp_field": "timestamp"
  },
  "search_settings": {
    "default_search_fields": ["full_message"]
  },
  "indexing_settings": {
    "commit_timeout_secs": 5,
    "docstore_compression_level": 8,
    "docstore_blocksize": 1000000,
    "split_num_docs_target": 10000000,
    "merge_policy": {
      "type": "stable_log",
      "min_level_num_docs": 100000,
      "merge_factor": 10,
      "max_merge_factor": 12,
      "maturation_period": "2days"
    },
    "resources": {
      "heap_size": "2.0 GB"
    }
  }
}
```