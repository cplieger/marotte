window.BENCHMARK_DATA = {
  "lastUpdate": 1791339265202,
  "repoUrl": "https://github.com/cplieger/ci",
  "entries": {
    "Benchmark": [
      {
        "commit": {
          "author": {
            "name": "cplieger",
            "username": "cplieger",
            "email": "917744+cplieger@users.noreply.github.com"
          },
          "committer": {
            "name": "Christopher Plieger",
            "username": "cplieger",
            "email": "917744+cplieger@users.noreply.github.com"
          },
          "id": "c3e91b6911fde478a90e1d1cf52e684fa491618e",
          "message": "test: silence per-iteration logging in benchmarks\n\nThree benchmarks logged once per operation through slog.Default(), which\nat benchmark iteration counts produced 4,498,652 of the weekly-bench\njob's 4,511,067 log lines, a 707 MB job log and a 265 MB uploaded\nartifact.\n\nquietLogs(b) swaps the default logger to slog.DiscardHandler for the\nbenchmark's duration, sharing the swap-and-restore with each package's\nexisting capture helper. The whole suite now emits 31 lines and 126 KB.",
          "timestamp": "2026-09-16T12:00:11Z",
          "url": "https://github.com/cplieger/vibekit/commit/c3e91b6911fde478a90e1d1cf52e684fa491618e"
        },
        "date": 1789570116076,
        "tool": "customSmallerIsBetter",
        "benches": [
          {
            "name": "BenchmarkBridgeManagerGetOrInsert/create - B/op",
            "value": 709,
            "range": "± 3.0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkBridgeManagerGetOrInsert/create - allocs/op",
            "value": 6,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkBridgeManagerGetOrInsert/create",
            "value": 809.15,
            "range": "± 31.7",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkBridgeManagerGetOrInsert/exists - B/op",
            "value": 8,
            "range": "± 0.0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkBridgeManagerGetOrInsert/exists - allocs/op",
            "value": 1,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkBridgeManagerGetOrInsert/exists",
            "value": 107.95,
            "range": "± 1.05",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkBridgeReadLoop - B/op",
            "value": 144946.5,
            "range": "± 105.0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkBridgeReadLoop - allocs/op",
            "value": 2754,
            "range": "± 1.5",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkBridgeReadLoop",
            "value": 942642,
            "range": "± 13273.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkBridgeRespond - B/op",
            "value": 752,
            "range": "± 0.0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkBridgeRespond - allocs/op",
            "value": 9,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkBridgeRespond",
            "value": 5595,
            "range": "± 145.0",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=262144/write=1024 - B/op",
            "value": 0,
            "range": "± 0.0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=262144/write=1024 - allocs/op",
            "value": 0,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=262144/write=1024",
            "value": 19.29,
            "range": "± 0.13",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=262144/write=64 - B/op",
            "value": 0,
            "range": "± 0.0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=262144/write=64 - allocs/op",
            "value": 0,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=262144/write=64",
            "value": 8.4485,
            "range": "± 0.127",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=262144/write=8192 - B/op",
            "value": 0,
            "range": "± 0.0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=262144/write=8192 - allocs/op",
            "value": 0,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=262144/write=8192",
            "value": 123.55,
            "range": "± 1.8",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=4096/write=1024 - B/op",
            "value": 0,
            "range": "± 0.0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=4096/write=1024 - allocs/op",
            "value": 0,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=4096/write=1024",
            "value": 16.77,
            "range": "± 0.09",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=4096/write=64 - B/op",
            "value": 0,
            "range": "± 0.0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=4096/write=64 - allocs/op",
            "value": 0,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=4096/write=64",
            "value": 8.6085,
            "range": "± 2.4435",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=4096/write=8192 - B/op",
            "value": 0,
            "range": "± 0.0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=4096/write=8192 - allocs/op",
            "value": 0,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=4096/write=8192",
            "value": 43.12,
            "range": "± 0.085",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=65536/write=1024 - B/op",
            "value": 0,
            "range": "± 0.0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=65536/write=1024 - allocs/op",
            "value": 0,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=65536/write=1024",
            "value": 19.035,
            "range": "± 0.07",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=65536/write=64 - B/op",
            "value": 0,
            "range": "± 0.0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=65536/write=64 - allocs/op",
            "value": 0,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=65536/write=64",
            "value": 8.445,
            "range": "± 0.149",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=65536/write=8192 - B/op",
            "value": 0,
            "range": "± 0.0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=65536/write=8192 - allocs/op",
            "value": 0,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=65536/write=8192",
            "value": 123.1,
            "range": "± 0.2",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkCensusMeta - B/op",
            "value": 552,
            "range": "± 0.0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkCensusMeta - allocs/op",
            "value": 11,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkCensusMeta",
            "value": 2108.5,
            "range": "± 17.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkCheapestModel/catalog_100 - B/op",
            "value": 0,
            "range": "± 0.0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkCheapestModel/catalog_100 - allocs/op",
            "value": 0,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkCheapestModel/catalog_100",
            "value": 725.4,
            "range": "± 2.4",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkCheapestModel/catalog_20 - B/op",
            "value": 0,
            "range": "± 0.0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkCheapestModel/catalog_20 - allocs/op",
            "value": 0,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkCheapestModel/catalog_20",
            "value": 151.2,
            "range": "± 0.3",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkCheapestModel/catalog_5 - B/op",
            "value": 0,
            "range": "± 0.0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkCheapestModel/catalog_5 - allocs/op",
            "value": 0,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkCheapestModel/catalog_5",
            "value": 37.475,
            "range": "± 0.09",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkDispatcherServeHTTP/dispatch - B/op",
            "value": 6835.5,
            "range": "± 0.5",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkDispatcherServeHTTP/dispatch - allocs/op",
            "value": 30,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkDispatcherServeHTTP/dispatch",
            "value": 4612.5,
            "range": "± 164.0",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkDispatcherServeHTTP/unknown_command - B/op",
            "value": 6932,
            "range": "± 0.5",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkDispatcherServeHTTP/unknown_command - allocs/op",
            "value": 30,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkDispatcherServeHTTP/unknown_command",
            "value": 4525,
            "range": "± 29.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkEmit - B/op",
            "value": 200,
            "unit": "B/op"
          },
          {
            "name": "BenchmarkEmit - allocs/op",
            "value": 4,
            "unit": "allocs/op"
          },
          {
            "name": "BenchmarkEmit",
            "value": 726.6,
            "unit": "ns/op"
          },
          {
            "name": "BenchmarkHandleAssistantChunk/long_chunk - B/op",
            "value": 13784836,
            "unit": "B/op"
          },
          {
            "name": "BenchmarkHandleAssistantChunk/long_chunk - allocs/op",
            "value": 25,
            "unit": "allocs/op"
          },
          {
            "name": "BenchmarkHandleAssistantChunk/long_chunk",
            "value": 1083302,
            "unit": "ns/op"
          },
          {
            "name": "BenchmarkHandleAssistantChunk/short_chunk - B/op",
            "value": 515502,
            "unit": "B/op"
          },
          {
            "name": "BenchmarkHandleAssistantChunk/short_chunk - allocs/op",
            "value": 23,
            "unit": "allocs/op"
          },
          {
            "name": "BenchmarkHandleAssistantChunk/short_chunk",
            "value": 70309,
            "unit": "ns/op"
          },
          {
            "name": "BenchmarkHandleAssistantChunk/with_reasoning - B/op",
            "value": 1174063,
            "unit": "B/op"
          },
          {
            "name": "BenchmarkHandleAssistantChunk/with_reasoning - allocs/op",
            "value": 20,
            "unit": "allocs/op"
          },
          {
            "name": "BenchmarkHandleAssistantChunk/with_reasoning",
            "value": 70415,
            "unit": "ns/op"
          },
          {
            "name": "BenchmarkHandleCommand/cache_hit - B/op",
            "value": 10255,
            "range": "± 1.0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkHandleCommand/cache_hit - allocs/op",
            "value": 52,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkHandleCommand/cache_hit",
            "value": 13256,
            "range": "± 200.0",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkHandleCommand/cancel - B/op",
            "value": 7165.5,
            "range": "± 6.5",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkHandleCommand/cancel - allocs/op",
            "value": 37,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkHandleCommand/cancel",
            "value": 6974.5,
            "range": "± 347.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkHandleCommand/create_chat - B/op",
            "value": 10466,
            "range": "± 6.5",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkHandleCommand/create_chat - allocs/op",
            "value": 58,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkHandleCommand/create_chat",
            "value": 14491,
            "range": "± 186.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkHandleCommand/prompt - B/op",
            "value": 94895.5,
            "range": "± 9230.5",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkHandleCommand/prompt - allocs/op",
            "value": 100.5,
            "range": "± 8.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkHandleCommand/prompt",
            "value": 136941,
            "range": "± 16641.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkIgnoreMatcherMatches/rules_15_deep - B/op",
            "value": 32576,
            "range": "± 0.0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkIgnoreMatcherMatches/rules_15_deep - allocs/op",
            "value": 131,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkIgnoreMatcherMatches/rules_15_deep",
            "value": 18443.5,
            "range": "± 116.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkIgnoreMatcherMatches/rules_15_medium - B/op",
            "value": 15888,
            "range": "± 0.0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkIgnoreMatcherMatches/rules_15_medium - allocs/op",
            "value": 66,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkIgnoreMatcherMatches/rules_15_medium",
            "value": 12151.5,
            "range": "± 64.0",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkIgnoreMatcherMatches/rules_15_shallow - B/op",
            "value": 5360,
            "range": "± 0.0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkIgnoreMatcherMatches/rules_15_shallow - allocs/op",
            "value": 25,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkIgnoreMatcherMatches/rules_15_shallow",
            "value": 6964,
            "range": "± 60.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkIgnoreMatcherMatches/rules_30_deep - B/op",
            "value": 66624,
            "range": "± 0.0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkIgnoreMatcherMatches/rules_30_deep - allocs/op",
            "value": 264,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkIgnoreMatcherMatches/rules_30_deep",
            "value": 33204.5,
            "range": "± 443.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkIgnoreMatcherMatches/rules_30_medium - B/op",
            "value": 32016,
            "range": "± 0.0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkIgnoreMatcherMatches/rules_30_medium - allocs/op",
            "value": 129,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkIgnoreMatcherMatches/rules_30_medium",
            "value": 20065.5,
            "range": "± 143.0",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkIgnoreMatcherMatches/rules_30_shallow - B/op",
            "value": 10224,
            "range": "± 0.0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkIgnoreMatcherMatches/rules_30_shallow - allocs/op",
            "value": 44,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkIgnoreMatcherMatches/rules_30_shallow",
            "value": 9492.5,
            "range": "± 82.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkIgnoreMatcherMatches/rules_5_deep - B/op",
            "value": 12608,
            "range": "± 0.0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkIgnoreMatcherMatches/rules_5_deep - allocs/op",
            "value": 53,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkIgnoreMatcherMatches/rules_5_deep",
            "value": 9692,
            "range": "± 81.0",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkIgnoreMatcherMatches/rules_5_medium - B/op",
            "value": 6416,
            "range": "± 0.0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkIgnoreMatcherMatches/rules_5_medium - allocs/op",
            "value": 29,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkIgnoreMatcherMatches/rules_5_medium",
            "value": 7400,
            "range": "± 40.0",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkIgnoreMatcherMatches/rules_5_shallow - B/op",
            "value": 2544,
            "range": "± 0.0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkIgnoreMatcherMatches/rules_5_shallow - allocs/op",
            "value": 14,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkIgnoreMatcherMatches/rules_5_shallow",
            "value": 5498,
            "range": "± 86.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkLineTrackerRecord/existing_1 - B/op",
            "value": 78,
            "range": "± 0.0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkLineTrackerRecord/existing_1 - allocs/op",
            "value": 0,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkLineTrackerRecord/existing_1",
            "value": 93.115,
            "range": "± 1.33",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkLineTrackerRecord/existing_200 - B/op",
            "value": 78,
            "range": "± 0.0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkLineTrackerRecord/existing_200 - allocs/op",
            "value": 0,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkLineTrackerRecord/existing_200",
            "value": 93.265,
            "range": "± 1.87",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkLineTrackerRecord/existing_50 - B/op",
            "value": 78,
            "range": "± 0.0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkLineTrackerRecord/existing_50 - allocs/op",
            "value": 0,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkLineTrackerRecord/existing_50",
            "value": 92.785,
            "range": "± 1.04",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkLineTrackerRecordFromDiffs/diffs_1 - B/op",
            "value": 158,
            "range": "± 0.0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkLineTrackerRecordFromDiffs/diffs_1 - allocs/op",
            "value": 2,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkLineTrackerRecordFromDiffs/diffs_1",
            "value": 190.85,
            "range": "± 3.3",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkLineTrackerRecordFromDiffs/diffs_10 - B/op",
            "value": 1580,
            "range": "± 0.0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkLineTrackerRecordFromDiffs/diffs_10 - allocs/op",
            "value": 20,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkLineTrackerRecordFromDiffs/diffs_10",
            "value": 2076,
            "range": "± 50.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkLineTrackerRecordFromDiffs/diffs_50 - B/op",
            "value": 7904,
            "range": "± 3.5",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkLineTrackerRecordFromDiffs/diffs_50 - allocs/op",
            "value": 100,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkLineTrackerRecordFromDiffs/diffs_50",
            "value": 10444.5,
            "range": "± 219.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkMCPRegistrySnapshot/servers=1 - B/op",
            "value": 160,
            "unit": "B/op"
          },
          {
            "name": "BenchmarkMCPRegistrySnapshot/servers=1 - allocs/op",
            "value": 1,
            "unit": "allocs/op"
          },
          {
            "name": "BenchmarkMCPRegistrySnapshot/servers=1",
            "value": 112.4,
            "unit": "ns/op"
          },
          {
            "name": "BenchmarkMCPRegistrySnapshot/servers=20 - B/op",
            "value": 3456,
            "unit": "B/op"
          },
          {
            "name": "BenchmarkMCPRegistrySnapshot/servers=20 - allocs/op",
            "value": 1,
            "unit": "allocs/op"
          },
          {
            "name": "BenchmarkMCPRegistrySnapshot/servers=20",
            "value": 2898,
            "unit": "ns/op"
          },
          {
            "name": "BenchmarkMCPRegistrySnapshot/servers=5 - B/op",
            "value": 896,
            "unit": "B/op"
          },
          {
            "name": "BenchmarkMCPRegistrySnapshot/servers=5 - allocs/op",
            "value": 1,
            "unit": "allocs/op"
          },
          {
            "name": "BenchmarkMCPRegistrySnapshot/servers=5",
            "value": 423.3,
            "unit": "ns/op"
          },
          {
            "name": "BenchmarkNormaliseRegistryResponse/servers=20 - B/op",
            "value": 32190,
            "range": "± 3.5",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkNormaliseRegistryResponse/servers=20 - allocs/op",
            "value": 329,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkNormaliseRegistryResponse/servers=20",
            "value": 99846.5,
            "range": "± 1085.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkNormaliseRegistryResponse/servers=5 - B/op",
            "value": 8022,
            "range": "± 1.0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkNormaliseRegistryResponse/servers=5 - allocs/op",
            "value": 87,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkNormaliseRegistryResponse/servers=5",
            "value": 25704.5,
            "range": "± 387.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkNormaliseRegistryResponse/servers=50 - B/op",
            "value": 63765.5,
            "range": "± 5.0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkNormaliseRegistryResponse/servers=50 - allocs/op",
            "value": 706,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkNormaliseRegistryResponse/servers=50",
            "value": 234205,
            "range": "± 3344.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkPushEncrypt - B/op",
            "value": 5736,
            "range": "± 0.0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkPushEncrypt - allocs/op",
            "value": 60,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkPushEncrypt",
            "value": 81143.5,
            "range": "± 1006.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkReadLoop_Notifications - B/op",
            "value": 66313.5,
            "range": "± 3.0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkReadLoop_Notifications - allocs/op",
            "value": 7,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkReadLoop_Notifications",
            "value": 10817.5,
            "range": "± 476.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkReadLoop_Responses/large - B/op",
            "value": 7605561,
            "range": "± 171.5",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkReadLoop_Responses/large - allocs/op",
            "value": 307,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkReadLoop_Responses/large",
            "value": 7860969,
            "range": "± 65459.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkReadLoop_Responses/medium - B/op",
            "value": 567526.5,
            "range": "± 13.0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkReadLoop_Responses/medium - allocs/op",
            "value": 304,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkReadLoop_Responses/medium",
            "value": 618876,
            "range": "± 8678.0",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkReadLoop_Responses/small - B/op",
            "value": 84214.5,
            "range": "± 5.5",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkReadLoop_Responses/small - allocs/op",
            "value": 304,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkReadLoop_Responses/small",
            "value": 101268.5,
            "range": "± 929.0",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkRegistryCacheGetOrFetch/hit_same_key - B/op",
            "value": 48,
            "range": "± 0.0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkRegistryCacheGetOrFetch/hit_same_key - allocs/op",
            "value": 1,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkRegistryCacheGetOrFetch/hit_same_key",
            "value": 108.5,
            "range": "± 4.25",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkRegistryCacheGetOrFetch/miss_different_keys - B/op",
            "value": 463,
            "range": "± 0.0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkRegistryCacheGetOrFetch/miss_different_keys - allocs/op",
            "value": 10,
            "range": "± 0.5",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkRegistryCacheGetOrFetch/miss_different_keys",
            "value": 8612,
            "range": "± 56.0",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkRegistryCacheGetOrFetch/miss_same_key_singleflight - B/op",
            "value": 48,
            "range": "± 0.0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkRegistryCacheGetOrFetch/miss_same_key_singleflight - allocs/op",
            "value": 1,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkRegistryCacheGetOrFetch/miss_same_key_singleflight",
            "value": 108.45,
            "range": "± 2.1",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkResolveInsideAbs/absolute_inside - B/op",
            "value": 2624,
            "range": "± 0.0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkResolveInsideAbs/absolute_inside - allocs/op",
            "value": 32,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkResolveInsideAbs/absolute_inside",
            "value": 14752.5,
            "range": "± 60.0",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkResolveInsideAbs/missing_parent - B/op",
            "value": 2704,
            "range": "± 0.0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkResolveInsideAbs/missing_parent - allocs/op",
            "value": 33,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkResolveInsideAbs/missing_parent",
            "value": 15142,
            "range": "± 116.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkResolveInsideAbs/relative - B/op",
            "value": 2656,
            "range": "± 0.0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkResolveInsideAbs/relative - allocs/op",
            "value": 33,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkResolveInsideAbs/relative",
            "value": 14868,
            "range": "± 148.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkResolvePath/allowed_path - B/op",
            "value": 2336,
            "range": "± 0.0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkResolvePath/allowed_path - allocs/op",
            "value": 28,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkResolvePath/allowed_path",
            "value": 14826,
            "range": "± 138.0",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkResolvePath/deep_nested_path - B/op",
            "value": 2352,
            "range": "± 0.0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkResolvePath/deep_nested_path - allocs/op",
            "value": 28,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkResolvePath/deep_nested_path",
            "value": 14852,
            "range": "± 108.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkResolvePath/outside_roots_path - B/op",
            "value": 40,
            "range": "± 0.0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkResolvePath/outside_roots_path - allocs/op",
            "value": 2,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkResolvePath/outside_roots_path",
            "value": 279.35,
            "range": "± 1.75",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkResolvePath/symlink_path - B/op",
            "value": 3856,
            "range": "± 0.0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkResolvePath/symlink_path - allocs/op",
            "value": 47,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkResolvePath/symlink_path",
            "value": 24177,
            "range": "± 83.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSanitizeOutput/ansi_heavy - B/op",
            "value": 15712,
            "range": "± 33.5",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSanitizeOutput/ansi_heavy - allocs/op",
            "value": 18,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSanitizeOutput/ansi_heavy",
            "value": 268438.5,
            "range": "± 2053.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSanitizeOutput/clean_short - B/op",
            "value": 241,
            "range": "± 0.0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSanitizeOutput/clean_short - allocs/op",
            "value": 3,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSanitizeOutput/clean_short",
            "value": 507.3,
            "range": "± 7.45",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSanitizeOutput/mixed_large - B/op",
            "value": 84489.5,
            "range": "± 44.5",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSanitizeOutput/mixed_large - allocs/op",
            "value": 20,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSanitizeOutput/mixed_large",
            "value": 113042,
            "range": "± 1217.0",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSanitizeOutput/unicode_heavy - B/op",
            "value": 18824.5,
            "range": "± 16.5",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSanitizeOutput/unicode_heavy - allocs/op",
            "value": 7,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSanitizeOutput/unicode_heavy",
            "value": 24351.5,
            "range": "± 183.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkScanKiroDirFS/20_docs - B/op",
            "value": 30240,
            "range": "± 0.0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkScanKiroDirFS/20_docs - allocs/op",
            "value": 210,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkScanKiroDirFS/20_docs",
            "value": 30348,
            "range": "± 570.0",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkScanKiroDirFS/50_docs - B/op",
            "value": 32664,
            "range": "± 0.0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkScanKiroDirFS/50_docs - allocs/op",
            "value": 211,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkScanKiroDirFS/50_docs",
            "value": 35926.5,
            "range": "± 408.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkScanKiroDirFS/5_docs - B/op",
            "value": 7688,
            "range": "± 0.0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkScanKiroDirFS/5_docs - allocs/op",
            "value": 71,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkScanKiroDirFS/5_docs",
            "value": 8993.5,
            "range": "± 164.0",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkScrubAuth/chained_16 - B/op",
            "value": 233,
            "range": "± 0.0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkScrubAuth/chained_16 - allocs/op",
            "value": 14,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkScrubAuth/chained_16",
            "value": 1904.5,
            "range": "± 30.0",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkScrubAuth/clean - B/op",
            "value": 529,
            "range": "± 0.5",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkScrubAuth/clean - allocs/op",
            "value": 9,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkScrubAuth/clean",
            "value": 4579,
            "range": "± 116.0",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkScrubAuth/long_output_1KB - B/op",
            "value": 6972.5,
            "range": "± 9.0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkScrubAuth/long_output_1KB - allocs/op",
            "value": 9,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkScrubAuth/long_output_1KB",
            "value": 59387.5,
            "range": "± 3296.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkScrubAuth/single_userinfo - B/op",
            "value": 497,
            "range": "± 1.0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkScrubAuth/single_userinfo - allocs/op",
            "value": 14,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkScrubAuth/single_userinfo",
            "value": 4303.5,
            "range": "± 21.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSecurityMiddleware/GET_headers_only - B/op",
            "value": 66,
            "range": "± 0.0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSecurityMiddleware/GET_headers_only - allocs/op",
            "value": 5,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSecurityMiddleware/GET_headers_only",
            "value": 280.6,
            "range": "± 4.55",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSecurityMiddleware/POST_same_origin - B/op",
            "value": 210,
            "range": "± 0.0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSecurityMiddleware/POST_same_origin - allocs/op",
            "value": 6,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSecurityMiddleware/POST_same_origin",
            "value": 544.1,
            "range": "± 3.25",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkStore_AppendMessage - B/op",
            "value": 2908210,
            "unit": "B/op"
          },
          {
            "name": "BenchmarkStore_AppendMessage - allocs/op",
            "value": 1082,
            "unit": "allocs/op"
          },
          {
            "name": "BenchmarkStore_AppendMessage",
            "value": 3641906,
            "unit": "ns/op"
          },
          {
            "name": "BenchmarkTranslateACPEvent/agent_message_chunk - B/op",
            "value": 1228021,
            "unit": "B/op"
          },
          {
            "name": "BenchmarkTranslateACPEvent/agent_message_chunk - allocs/op",
            "value": 28,
            "unit": "allocs/op"
          },
          {
            "name": "BenchmarkTranslateACPEvent/agent_message_chunk",
            "value": 78620,
            "unit": "ns/op"
          },
          {
            "name": "BenchmarkTranslateACPEvent/tool_call - B/op",
            "value": 4747,
            "unit": "B/op"
          },
          {
            "name": "BenchmarkTranslateACPEvent/tool_call - allocs/op",
            "value": 35,
            "unit": "allocs/op"
          },
          {
            "name": "BenchmarkTranslateACPEvent/tool_call",
            "value": 9595,
            "unit": "ns/op"
          },
          {
            "name": "BenchmarkTranslateACPEvent/tool_call_update - B/op",
            "value": 976,
            "unit": "B/op"
          },
          {
            "name": "BenchmarkTranslateACPEvent/tool_call_update - allocs/op",
            "value": 12,
            "unit": "allocs/op"
          },
          {
            "name": "BenchmarkTranslateACPEvent/tool_call_update",
            "value": 4631,
            "unit": "ns/op"
          },
          {
            "name": "BenchmarkTranslator_FullTurn - B/op",
            "value": 180978.5,
            "range": "± 1114.5",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkTranslator_FullTurn - allocs/op",
            "value": 1554,
            "range": "± 3.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkTranslator_FullTurn",
            "value": 219521.5,
            "range": "± 4879.0",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkTranslator_HandleAssistantChunk - B/op",
            "value": 421635.5,
            "range": "± 22790.0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkTranslator_HandleAssistantChunk - allocs/op",
            "value": 13,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkTranslator_HandleAssistantChunk",
            "value": 101630,
            "range": "± 7197.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkTranslator_HandleToolCall - B/op",
            "value": 3161.5,
            "range": "± 153.5",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkTranslator_HandleToolCall - allocs/op",
            "value": 17,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkTranslator_HandleToolCall",
            "value": 3938.5,
            "range": "± 200.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkTranslator_HandleUsageUpdate - B/op",
            "value": 56,
            "range": "± 0.0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkTranslator_HandleUsageUpdate - allocs/op",
            "value": 2,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkTranslator_HandleUsageUpdate",
            "value": 502.5,
            "range": "± 2.7",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkUtilityBridge_DrainResponse/chunks=100 - B/op",
            "value": 5000,
            "range": "± 0.0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkUtilityBridge_DrainResponse/chunks=100 - allocs/op",
            "value": 114,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkUtilityBridge_DrainResponse/chunks=100",
            "value": 90327,
            "range": "± 1150.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkUtilityBridge_DrainResponse/chunks=1000 - B/op",
            "value": 33992,
            "range": "± 0.5",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkUtilityBridge_DrainResponse/chunks=1000 - allocs/op",
            "value": 1014,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkUtilityBridge_DrainResponse/chunks=1000",
            "value": 887604.5,
            "range": "± 7586.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkUtilityBridge_DrainResponse/chunks=20 - B/op",
            "value": 2248,
            "range": "± 0.0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkUtilityBridge_DrainResponse/chunks=20 - allocs/op",
            "value": 34,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkUtilityBridge_DrainResponse/chunks=20",
            "value": 19357.5,
            "range": "± 151.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkUtilityBridge_DrainResponse/chunks=5 - B/op",
            "value": 1768,
            "range": "± 0.0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkUtilityBridge_DrainResponse/chunks=5 - allocs/op",
            "value": 19,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkUtilityBridge_DrainResponse/chunks=5",
            "value": 5935.5,
            "range": "± 137.0",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkUtilityBridge_DrainResponse/chunks=500 - B/op",
            "value": 17800,
            "range": "± 0.0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkUtilityBridge_DrainResponse/chunks=500 - allocs/op",
            "value": 514,
            "range": "± 0.0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkUtilityBridge_DrainResponse/chunks=500",
            "value": 448320.5,
            "range": "± 4127.0",
            "unit": "ns/op",
            "extra": "10 samples, median"
          }
        ]
      },
      {
        "commit": {
          "author": {
            "name": "Christopher Plieger",
            "username": "cplieger",
            "email": "917744+cplieger@users.noreply.github.com"
          },
          "committer": {
            "name": "GitHub",
            "username": "web-flow",
            "email": "noreply@github.com"
          },
          "id": "e649f74b9994c77ff30ed24a704810d9cdba8693",
          "message": "chore(devdeps): update dependency @types/node to v25.9.8 (#1319)",
          "timestamp": "2026-09-22T01:25:37Z",
          "url": "https://github.com/cplieger/vibekit/commit/e649f74b9994c77ff30ed24a704810d9cdba8693"
        },
        "date": 1790125899637,
        "tool": "customSmallerIsBetter",
        "benches": [
          {
            "name": "BenchmarkBridgeManagerGetOrInsert/create - B/op",
            "value": 720,
            "range": "± 15",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkBridgeManagerGetOrInsert/create - allocs/op",
            "value": 6,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkBridgeManagerGetOrInsert/create",
            "value": 1015.1,
            "range": "± 145.3",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkBridgeManagerGetOrInsert/exists - B/op",
            "value": 8,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkBridgeManagerGetOrInsert/exists - allocs/op",
            "value": 1,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkBridgeManagerGetOrInsert/exists",
            "value": 85.985,
            "range": "± 8.48",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkBridgeReadLoop - B/op",
            "value": 144915,
            "range": "± 101.5",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkBridgeReadLoop - allocs/op",
            "value": 2753,
            "range": "± 1",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkBridgeReadLoop",
            "value": 975407.5,
            "range": "± 7545",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkBridgeRespond - B/op",
            "value": 752,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkBridgeRespond - allocs/op",
            "value": 9,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkBridgeRespond",
            "value": 6293.5,
            "range": "± 348",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=262144/write=1024 - B/op",
            "value": 0,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=262144/write=1024 - allocs/op",
            "value": 0,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=262144/write=1024",
            "value": 22.105,
            "range": "± 0.92",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=262144/write=64 - B/op",
            "value": 0,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=262144/write=64 - allocs/op",
            "value": 0,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=262144/write=64",
            "value": 8.6485,
            "range": "± 0.227",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=262144/write=8192 - B/op",
            "value": 0,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=262144/write=8192 - allocs/op",
            "value": 0,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=262144/write=8192",
            "value": 124.7,
            "range": "± 1.8",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=4096/write=1024 - B/op",
            "value": 0,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=4096/write=1024 - allocs/op",
            "value": 0,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=4096/write=1024",
            "value": 18.61,
            "range": "± 1.48",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=4096/write=64 - B/op",
            "value": 0,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=4096/write=64 - allocs/op",
            "value": 0,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=4096/write=64",
            "value": 8.687,
            "range": "± 1.534",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=4096/write=8192 - B/op",
            "value": 0,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=4096/write=8192 - allocs/op",
            "value": 0,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=4096/write=8192",
            "value": 43.175,
            "range": "± 2.435",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=65536/write=1024 - B/op",
            "value": 0,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=65536/write=1024 - allocs/op",
            "value": 0,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=65536/write=1024",
            "value": 21.85,
            "range": "± 0.725",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=65536/write=64 - B/op",
            "value": 0,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=65536/write=64 - allocs/op",
            "value": 0,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=65536/write=64",
            "value": 8.5165,
            "range": "± 0.223",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=65536/write=8192 - B/op",
            "value": 0,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=65536/write=8192 - allocs/op",
            "value": 0,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=65536/write=8192",
            "value": 123.65,
            "range": "± 0.55",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkCensusMeta - B/op",
            "value": 552,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkCensusMeta - allocs/op",
            "value": 11,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkCensusMeta",
            "value": 2030,
            "range": "± 119.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkCheapestModel/catalog_100 - B/op",
            "value": 0,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkCheapestModel/catalog_100 - allocs/op",
            "value": 0,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkCheapestModel/catalog_100",
            "value": 727.3,
            "range": "± 5.9",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkCheapestModel/catalog_20 - B/op",
            "value": 0,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkCheapestModel/catalog_20 - allocs/op",
            "value": 0,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkCheapestModel/catalog_20",
            "value": 151.55,
            "range": "± 0.6",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkCheapestModel/catalog_5 - B/op",
            "value": 0,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkCheapestModel/catalog_5 - allocs/op",
            "value": 0,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkCheapestModel/catalog_5",
            "value": 37.61,
            "range": "± 1.045",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkDispatcherServeHTTP/dispatch - B/op",
            "value": 6836,
            "range": "± 1",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkDispatcherServeHTTP/dispatch - allocs/op",
            "value": 30,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkDispatcherServeHTTP/dispatch",
            "value": 4757,
            "range": "± 664.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkDispatcherServeHTTP/unknown_command - B/op",
            "value": 6932,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkDispatcherServeHTTP/unknown_command - allocs/op",
            "value": 30,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkDispatcherServeHTTP/unknown_command",
            "value": 4612.5,
            "range": "± 41",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkEmit - B/op",
            "value": 200,
            "unit": "B/op"
          },
          {
            "name": "BenchmarkEmit - allocs/op",
            "value": 4,
            "unit": "allocs/op"
          },
          {
            "name": "BenchmarkEmit",
            "value": 715,
            "unit": "ns/op"
          },
          {
            "name": "BenchmarkHandleAssistantChunk/long_chunk - B/op",
            "value": 13784669,
            "unit": "B/op"
          },
          {
            "name": "BenchmarkHandleAssistantChunk/long_chunk - allocs/op",
            "value": 25,
            "unit": "allocs/op"
          },
          {
            "name": "BenchmarkHandleAssistantChunk/long_chunk",
            "value": 1504291,
            "unit": "ns/op"
          },
          {
            "name": "BenchmarkHandleAssistantChunk/short_chunk - B/op",
            "value": 452810,
            "unit": "B/op"
          },
          {
            "name": "BenchmarkHandleAssistantChunk/short_chunk - allocs/op",
            "value": 23,
            "unit": "allocs/op"
          },
          {
            "name": "BenchmarkHandleAssistantChunk/short_chunk",
            "value": 69664,
            "unit": "ns/op"
          },
          {
            "name": "BenchmarkHandleAssistantChunk/with_reasoning - B/op",
            "value": 761429,
            "unit": "B/op"
          },
          {
            "name": "BenchmarkHandleAssistantChunk/with_reasoning - allocs/op",
            "value": 20,
            "unit": "allocs/op"
          },
          {
            "name": "BenchmarkHandleAssistantChunk/with_reasoning",
            "value": 60462,
            "unit": "ns/op"
          },
          {
            "name": "BenchmarkHandleCommand/cache_hit - B/op",
            "value": 10255,
            "range": "± 0.5",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkHandleCommand/cache_hit - allocs/op",
            "value": 52,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkHandleCommand/cache_hit",
            "value": 13574.5,
            "range": "± 464",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkHandleCommand/cancel - B/op",
            "value": 7165,
            "range": "± 5",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkHandleCommand/cancel - allocs/op",
            "value": 37,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkHandleCommand/cancel",
            "value": 7028,
            "range": "± 304.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkHandleCommand/create_chat - B/op",
            "value": 10466,
            "range": "± 14.5",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkHandleCommand/create_chat - allocs/op",
            "value": 58,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkHandleCommand/create_chat",
            "value": 14710.5,
            "range": "± 924",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkHandleCommand/prompt - B/op",
            "value": 87314,
            "range": "± 7603",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkHandleCommand/prompt - allocs/op",
            "value": 94,
            "range": "± 5.5",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkHandleCommand/prompt",
            "value": 124752.5,
            "range": "± 14004",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkIgnoreMatcherMatches/rules_15_deep - B/op",
            "value": 32576,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkIgnoreMatcherMatches/rules_15_deep - allocs/op",
            "value": 131,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkIgnoreMatcherMatches/rules_15_deep",
            "value": 18183,
            "range": "± 2284.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkIgnoreMatcherMatches/rules_15_medium - B/op",
            "value": 15888,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkIgnoreMatcherMatches/rules_15_medium - allocs/op",
            "value": 66,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkIgnoreMatcherMatches/rules_15_medium",
            "value": 11794.5,
            "range": "± 96.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkIgnoreMatcherMatches/rules_15_shallow - B/op",
            "value": 5360,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkIgnoreMatcherMatches/rules_15_shallow - allocs/op",
            "value": 25,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkIgnoreMatcherMatches/rules_15_shallow",
            "value": 6602.5,
            "range": "± 182.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkIgnoreMatcherMatches/rules_30_deep - B/op",
            "value": 66624,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkIgnoreMatcherMatches/rules_30_deep - allocs/op",
            "value": 264,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkIgnoreMatcherMatches/rules_30_deep",
            "value": 33146,
            "range": "± 132.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkIgnoreMatcherMatches/rules_30_medium - B/op",
            "value": 32016,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkIgnoreMatcherMatches/rules_30_medium - allocs/op",
            "value": 129,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkIgnoreMatcherMatches/rules_30_medium",
            "value": 19562,
            "range": "± 77",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkIgnoreMatcherMatches/rules_30_shallow - B/op",
            "value": 10224,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkIgnoreMatcherMatches/rules_30_shallow - allocs/op",
            "value": 44,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkIgnoreMatcherMatches/rules_30_shallow",
            "value": 9036,
            "range": "± 43",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkIgnoreMatcherMatches/rules_5_deep - B/op",
            "value": 12608,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkIgnoreMatcherMatches/rules_5_deep - allocs/op",
            "value": 53,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkIgnoreMatcherMatches/rules_5_deep",
            "value": 9364.5,
            "range": "± 45",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkIgnoreMatcherMatches/rules_5_medium - B/op",
            "value": 6416,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkIgnoreMatcherMatches/rules_5_medium - allocs/op",
            "value": 29,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkIgnoreMatcherMatches/rules_5_medium",
            "value": 7101.5,
            "range": "± 612.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkIgnoreMatcherMatches/rules_5_shallow - B/op",
            "value": 2544,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkIgnoreMatcherMatches/rules_5_shallow - allocs/op",
            "value": 14,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkIgnoreMatcherMatches/rules_5_shallow",
            "value": 5155,
            "range": "± 76.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkLineTrackerRecord/existing_1 - B/op",
            "value": 78,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkLineTrackerRecord/existing_1 - allocs/op",
            "value": 0,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkLineTrackerRecord/existing_1",
            "value": 92.91,
            "range": "± 12.915",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkLineTrackerRecord/existing_200 - B/op",
            "value": 78,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkLineTrackerRecord/existing_200 - allocs/op",
            "value": 0,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkLineTrackerRecord/existing_200",
            "value": 92.26,
            "range": "± 2.66",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkLineTrackerRecord/existing_50 - B/op",
            "value": 78,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkLineTrackerRecord/existing_50 - allocs/op",
            "value": 0,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkLineTrackerRecord/existing_50",
            "value": 92.595,
            "range": "± 1.4",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkLineTrackerRecordFromDiffs/diffs_1 - B/op",
            "value": 158,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkLineTrackerRecordFromDiffs/diffs_1 - allocs/op",
            "value": 2,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkLineTrackerRecordFromDiffs/diffs_1",
            "value": 191.3,
            "range": "± 13.6",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkLineTrackerRecordFromDiffs/diffs_10 - B/op",
            "value": 1580,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkLineTrackerRecordFromDiffs/diffs_10 - allocs/op",
            "value": 20,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkLineTrackerRecordFromDiffs/diffs_10",
            "value": 2070.5,
            "range": "± 54.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkLineTrackerRecordFromDiffs/diffs_50 - B/op",
            "value": 7906,
            "range": "± 3.5",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkLineTrackerRecordFromDiffs/diffs_50 - allocs/op",
            "value": 100,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkLineTrackerRecordFromDiffs/diffs_50",
            "value": 10617.5,
            "range": "± 155",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkMCPRegistrySnapshot/servers=1 - B/op",
            "value": 160,
            "unit": "B/op"
          },
          {
            "name": "BenchmarkMCPRegistrySnapshot/servers=1 - allocs/op",
            "value": 1,
            "unit": "allocs/op"
          },
          {
            "name": "BenchmarkMCPRegistrySnapshot/servers=1",
            "value": 114,
            "unit": "ns/op"
          },
          {
            "name": "BenchmarkMCPRegistrySnapshot/servers=20 - B/op",
            "value": 3456,
            "unit": "B/op"
          },
          {
            "name": "BenchmarkMCPRegistrySnapshot/servers=20 - allocs/op",
            "value": 1,
            "unit": "allocs/op"
          },
          {
            "name": "BenchmarkMCPRegistrySnapshot/servers=20",
            "value": 2972,
            "unit": "ns/op"
          },
          {
            "name": "BenchmarkMCPRegistrySnapshot/servers=5 - B/op",
            "value": 896,
            "unit": "B/op"
          },
          {
            "name": "BenchmarkMCPRegistrySnapshot/servers=5 - allocs/op",
            "value": 1,
            "unit": "allocs/op"
          },
          {
            "name": "BenchmarkMCPRegistrySnapshot/servers=5",
            "value": 427.2,
            "unit": "ns/op"
          },
          {
            "name": "BenchmarkNormaliseRegistryResponse/servers=20 - B/op",
            "value": 32189,
            "range": "± 5",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkNormaliseRegistryResponse/servers=20 - allocs/op",
            "value": 329,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkNormaliseRegistryResponse/servers=20",
            "value": 97309.5,
            "range": "± 358.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkNormaliseRegistryResponse/servers=5 - B/op",
            "value": 8022.5,
            "range": "± 1.5",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkNormaliseRegistryResponse/servers=5 - allocs/op",
            "value": 87,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkNormaliseRegistryResponse/servers=5",
            "value": 25043,
            "range": "± 139",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkNormaliseRegistryResponse/servers=50 - B/op",
            "value": 63762.5,
            "range": "± 7",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkNormaliseRegistryResponse/servers=50 - allocs/op",
            "value": 706,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkNormaliseRegistryResponse/servers=50",
            "value": 227868,
            "range": "± 6793",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkPushEncrypt - B/op",
            "value": 5736,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkPushEncrypt - allocs/op",
            "value": 60,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkPushEncrypt",
            "value": 80669.5,
            "range": "± 2724",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkReadLoop_Notifications - B/op",
            "value": 66315,
            "range": "± 3",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkReadLoop_Notifications - allocs/op",
            "value": 7,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkReadLoop_Notifications",
            "value": 11797,
            "range": "± 741",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkReadLoop_Responses/large - B/op",
            "value": 7605402,
            "range": "± 132",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkReadLoop_Responses/large - allocs/op",
            "value": 307,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkReadLoop_Responses/large",
            "value": 7654916.5,
            "range": "± 464054",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkReadLoop_Responses/medium - B/op",
            "value": 567529,
            "range": "± 23.5",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkReadLoop_Responses/medium - allocs/op",
            "value": 304,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkReadLoop_Responses/medium",
            "value": 626217,
            "range": "± 9344",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkReadLoop_Responses/small - B/op",
            "value": 84214,
            "range": "± 3.5",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkReadLoop_Responses/small - allocs/op",
            "value": 304,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkReadLoop_Responses/small",
            "value": 100729,
            "range": "± 708",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkRegistryCacheGetOrFetch/hit_same_key - B/op",
            "value": 48,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkRegistryCacheGetOrFetch/hit_same_key - allocs/op",
            "value": 1,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkRegistryCacheGetOrFetch/hit_same_key",
            "value": 106.55,
            "range": "± 4.3",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkRegistryCacheGetOrFetch/miss_different_keys - B/op",
            "value": 463,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkRegistryCacheGetOrFetch/miss_different_keys - allocs/op",
            "value": 9,
            "range": "± 0.5",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkRegistryCacheGetOrFetch/miss_different_keys",
            "value": 8406,
            "range": "± 59.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkRegistryCacheGetOrFetch/miss_same_key_singleflight - B/op",
            "value": 48,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkRegistryCacheGetOrFetch/miss_same_key_singleflight - allocs/op",
            "value": 1,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkRegistryCacheGetOrFetch/miss_same_key_singleflight",
            "value": 106.4,
            "range": "± 8",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkResolveInsideAbs/absolute_inside - B/op",
            "value": 2624,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkResolveInsideAbs/absolute_inside - allocs/op",
            "value": 32,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkResolveInsideAbs/absolute_inside",
            "value": 15796,
            "range": "± 62",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkResolveInsideAbs/missing_parent - B/op",
            "value": 2704,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkResolveInsideAbs/missing_parent - allocs/op",
            "value": 33,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkResolveInsideAbs/missing_parent",
            "value": 16071.5,
            "range": "± 62.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkResolveInsideAbs/relative - B/op",
            "value": 2656,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkResolveInsideAbs/relative - allocs/op",
            "value": 33,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkResolveInsideAbs/relative",
            "value": 15937.5,
            "range": "± 264.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkResolvePath/allowed_path - B/op",
            "value": 2336,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkResolvePath/allowed_path - allocs/op",
            "value": 28,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkResolvePath/allowed_path",
            "value": 13746.5,
            "range": "± 209",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkResolvePath/deep_nested_path - B/op",
            "value": 2352,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkResolvePath/deep_nested_path - allocs/op",
            "value": 28,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkResolvePath/deep_nested_path",
            "value": 13831,
            "range": "± 64",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkResolvePath/outside_roots_path - B/op",
            "value": 40,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkResolvePath/outside_roots_path - allocs/op",
            "value": 2,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkResolvePath/outside_roots_path",
            "value": 280.2,
            "range": "± 7.2",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkResolvePath/symlink_path - B/op",
            "value": 3856,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkResolvePath/symlink_path - allocs/op",
            "value": 47,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkResolvePath/symlink_path",
            "value": 22571.5,
            "range": "± 796.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSanitizeOutput/ansi_heavy - B/op",
            "value": 15699,
            "range": "± 21.5",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSanitizeOutput/ansi_heavy - allocs/op",
            "value": 18,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSanitizeOutput/ansi_heavy",
            "value": 266904,
            "range": "± 7085",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSanitizeOutput/clean_short - B/op",
            "value": 241,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSanitizeOutput/clean_short - allocs/op",
            "value": 3,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSanitizeOutput/clean_short",
            "value": 503.75,
            "range": "± 9.85",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSanitizeOutput/mixed_large - B/op",
            "value": 84546.5,
            "range": "± 85.5",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSanitizeOutput/mixed_large - allocs/op",
            "value": 20,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSanitizeOutput/mixed_large",
            "value": 109490,
            "range": "± 573.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSanitizeOutput/unicode_heavy - B/op",
            "value": 18849.5,
            "range": "± 9",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSanitizeOutput/unicode_heavy - allocs/op",
            "value": 7,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSanitizeOutput/unicode_heavy",
            "value": 23626,
            "range": "± 63",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkScanKiroDirFS/20_docs - B/op",
            "value": 30240,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkScanKiroDirFS/20_docs - allocs/op",
            "value": 210,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkScanKiroDirFS/20_docs",
            "value": 29554.5,
            "range": "± 251",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkScanKiroDirFS/50_docs - B/op",
            "value": 32664,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkScanKiroDirFS/50_docs - allocs/op",
            "value": 211,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkScanKiroDirFS/50_docs",
            "value": 35024,
            "range": "± 277",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkScanKiroDirFS/5_docs - B/op",
            "value": 7688,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkScanKiroDirFS/5_docs - allocs/op",
            "value": 71,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkScanKiroDirFS/5_docs",
            "value": 8680,
            "range": "± 79",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkScrubAuth/chained_16 - B/op",
            "value": 233,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkScrubAuth/chained_16 - allocs/op",
            "value": 14,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkScrubAuth/chained_16",
            "value": 1894.5,
            "range": "± 39",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkScrubAuth/clean - B/op",
            "value": 530,
            "range": "± 0.5",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkScrubAuth/clean - allocs/op",
            "value": 9,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkScrubAuth/clean",
            "value": 4668,
            "range": "± 109",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkScrubAuth/long_output_1KB - B/op",
            "value": 6969,
            "range": "± 6.5",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkScrubAuth/long_output_1KB - allocs/op",
            "value": 9,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkScrubAuth/long_output_1KB",
            "value": 59177.5,
            "range": "± 2853",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkScrubAuth/single_userinfo - B/op",
            "value": 497.5,
            "range": "± 0.5",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkScrubAuth/single_userinfo - allocs/op",
            "value": 14,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkScrubAuth/single_userinfo",
            "value": 4328.5,
            "range": "± 133.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSecurityMiddleware/GET_headers_only - B/op",
            "value": 66,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSecurityMiddleware/GET_headers_only - allocs/op",
            "value": 5,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSecurityMiddleware/GET_headers_only",
            "value": 278.6,
            "range": "± 16.65",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSecurityMiddleware/POST_same_origin - B/op",
            "value": 210,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSecurityMiddleware/POST_same_origin - allocs/op",
            "value": 6,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSecurityMiddleware/POST_same_origin",
            "value": 534.6,
            "range": "± 15.85",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkStore_AppendMessage - B/op",
            "value": 10053653,
            "unit": "B/op"
          },
          {
            "name": "BenchmarkStore_AppendMessage - allocs/op",
            "value": 3002,
            "unit": "allocs/op"
          },
          {
            "name": "BenchmarkStore_AppendMessage",
            "value": 8795582,
            "unit": "ns/op"
          },
          {
            "name": "BenchmarkTranslateACPEvent/agent_message_chunk - B/op",
            "value": 1069780,
            "unit": "B/op"
          },
          {
            "name": "BenchmarkTranslateACPEvent/agent_message_chunk - allocs/op",
            "value": 28,
            "unit": "allocs/op"
          },
          {
            "name": "BenchmarkTranslateACPEvent/agent_message_chunk",
            "value": 76553,
            "unit": "ns/op"
          },
          {
            "name": "BenchmarkTranslateACPEvent/tool_call - B/op",
            "value": 4752,
            "unit": "B/op"
          },
          {
            "name": "BenchmarkTranslateACPEvent/tool_call - allocs/op",
            "value": 35,
            "unit": "allocs/op"
          },
          {
            "name": "BenchmarkTranslateACPEvent/tool_call",
            "value": 9975,
            "unit": "ns/op"
          },
          {
            "name": "BenchmarkTranslateACPEvent/tool_call_update - B/op",
            "value": 976,
            "unit": "B/op"
          },
          {
            "name": "BenchmarkTranslateACPEvent/tool_call_update - allocs/op",
            "value": 12,
            "unit": "allocs/op"
          },
          {
            "name": "BenchmarkTranslateACPEvent/tool_call_update",
            "value": 4701,
            "unit": "ns/op"
          },
          {
            "name": "BenchmarkTranslator_FullTurn - B/op",
            "value": 180581.5,
            "range": "± 903.5",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkTranslator_FullTurn - allocs/op",
            "value": 1554,
            "range": "± 3.5",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkTranslator_FullTurn",
            "value": 216005.5,
            "range": "± 2823",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkTranslator_HandleAssistantChunk - B/op",
            "value": 480748.5,
            "range": "± 104298",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkTranslator_HandleAssistantChunk - allocs/op",
            "value": 13,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkTranslator_HandleAssistantChunk",
            "value": 96066,
            "range": "± 7190.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkTranslator_HandleToolCall - B/op",
            "value": 3104.5,
            "range": "± 125.5",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkTranslator_HandleToolCall - allocs/op",
            "value": 17,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkTranslator_HandleToolCall",
            "value": 4492.5,
            "range": "± 839",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkTranslator_HandleUsageUpdate - B/op",
            "value": 56,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkTranslator_HandleUsageUpdate - allocs/op",
            "value": 2,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkTranslator_HandleUsageUpdate",
            "value": 486.2,
            "range": "± 2.8",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkUtilityBridge_DrainResponse/chunks=100 - B/op",
            "value": 5000,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkUtilityBridge_DrainResponse/chunks=100 - allocs/op",
            "value": 114,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkUtilityBridge_DrainResponse/chunks=100",
            "value": 90246,
            "range": "± 1253",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkUtilityBridge_DrainResponse/chunks=1000 - B/op",
            "value": 33992,
            "range": "± 0.5",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkUtilityBridge_DrainResponse/chunks=1000 - allocs/op",
            "value": 1014,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkUtilityBridge_DrainResponse/chunks=1000",
            "value": 887691,
            "range": "± 4360",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkUtilityBridge_DrainResponse/chunks=20 - B/op",
            "value": 2248,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkUtilityBridge_DrainResponse/chunks=20 - allocs/op",
            "value": 34,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkUtilityBridge_DrainResponse/chunks=20",
            "value": 19282,
            "range": "± 96",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkUtilityBridge_DrainResponse/chunks=5 - B/op",
            "value": 1768,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkUtilityBridge_DrainResponse/chunks=5 - allocs/op",
            "value": 19,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkUtilityBridge_DrainResponse/chunks=5",
            "value": 5946,
            "range": "± 128.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkUtilityBridge_DrainResponse/chunks=500 - B/op",
            "value": 17800,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkUtilityBridge_DrainResponse/chunks=500 - allocs/op",
            "value": 514,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkUtilityBridge_DrainResponse/chunks=500",
            "value": 444654,
            "range": "± 2183.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          }
        ]
      },
      {
        "commit": {
          "author": {
            "name": "Christopher Plieger",
            "username": "cplieger",
            "email": "917744+cplieger@users.noreply.github.com"
          },
          "committer": {
            "name": "GitHub",
            "username": "web-flow",
            "email": "noreply@github.com"
          },
          "id": "ec692c4f6ae5b4abd23348ae3edb81d02f87dd6d",
          "message": "chore(deps): update kiro-cli to v2.26.0 (#1332)",
          "timestamp": "2026-09-29T19:30:00Z",
          "url": "https://github.com/cplieger/vibekit/commit/ec692c4f6ae5b4abd23348ae3edb81d02f87dd6d"
        },
        "date": 1790734199740,
        "tool": "customSmallerIsBetter",
        "benches": [
          {
            "name": "BenchmarkBridgeManagerGetOrInsert/create - B/op",
            "value": 703,
            "range": "± 22.5",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkBridgeManagerGetOrInsert/create - allocs/op",
            "value": 6,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkBridgeManagerGetOrInsert/create",
            "value": 872.4,
            "range": "± 114.95",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkBridgeManagerGetOrInsert/exists - B/op",
            "value": 8,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkBridgeManagerGetOrInsert/exists - allocs/op",
            "value": 1,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkBridgeManagerGetOrInsert/exists",
            "value": 85.565,
            "range": "± 2.955",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkBridgeReadLoop - B/op",
            "value": 144565,
            "range": "± 147.5",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkBridgeReadLoop - allocs/op",
            "value": 2750.5,
            "range": "± 2",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkBridgeReadLoop",
            "value": 832760.5,
            "range": "± 7369",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkBridgeRespond - B/op",
            "value": 752,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkBridgeRespond - allocs/op",
            "value": 9,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkBridgeRespond",
            "value": 3806.5,
            "range": "± 180",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=262144/write=1024 - B/op",
            "value": 0,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=262144/write=1024 - allocs/op",
            "value": 0,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=262144/write=1024",
            "value": 15.38,
            "range": "± 0.12",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=262144/write=64 - B/op",
            "value": 0,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=262144/write=64 - allocs/op",
            "value": 0,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=262144/write=64",
            "value": 5.8015,
            "range": "± 0.027",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=262144/write=8192 - B/op",
            "value": 0,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=262144/write=8192 - allocs/op",
            "value": 0,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=262144/write=8192",
            "value": 78.24,
            "range": "± 0.37",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=4096/write=1024 - B/op",
            "value": 0,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=4096/write=1024 - allocs/op",
            "value": 0,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=4096/write=1024",
            "value": 15.06,
            "range": "± 0.64",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=4096/write=64 - B/op",
            "value": 0,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=4096/write=64 - allocs/op",
            "value": 0,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=4096/write=64",
            "value": 5.8995,
            "range": "± 0.745",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=4096/write=8192 - B/op",
            "value": 0,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=4096/write=8192 - allocs/op",
            "value": 0,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=4096/write=8192",
            "value": 37.42,
            "range": "± 0.48",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=65536/write=1024 - B/op",
            "value": 0,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=65536/write=1024 - allocs/op",
            "value": 0,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=65536/write=1024",
            "value": 15.88,
            "range": "± 0.34",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=65536/write=64 - B/op",
            "value": 0,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=65536/write=64 - allocs/op",
            "value": 0,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=65536/write=64",
            "value": 5.854,
            "range": "± 0.0665",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=65536/write=8192 - B/op",
            "value": 0,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=65536/write=8192 - allocs/op",
            "value": 0,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=65536/write=8192",
            "value": 76.06,
            "range": "± 2.11",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkCensusMeta - B/op",
            "value": 552,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkCensusMeta - allocs/op",
            "value": 11,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkCensusMeta",
            "value": 1492.5,
            "range": "± 32.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkCheapestModel/catalog_100 - B/op",
            "value": 0,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkCheapestModel/catalog_100 - allocs/op",
            "value": 0,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkCheapestModel/catalog_100",
            "value": 522.55,
            "range": "± 14",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkCheapestModel/catalog_20 - B/op",
            "value": 0,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkCheapestModel/catalog_20 - allocs/op",
            "value": 0,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkCheapestModel/catalog_20",
            "value": 106.15,
            "range": "± 3.15",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkCheapestModel/catalog_5 - B/op",
            "value": 0,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkCheapestModel/catalog_5 - allocs/op",
            "value": 0,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkCheapestModel/catalog_5",
            "value": 28.17,
            "range": "± 0.61",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkDispatcherServeHTTP/dispatch - B/op",
            "value": 6836,
            "range": "± 0.5",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkDispatcherServeHTTP/dispatch - allocs/op",
            "value": 30,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkDispatcherServeHTTP/dispatch",
            "value": 3375,
            "range": "± 106",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkDispatcherServeHTTP/unknown_command - B/op",
            "value": 6932.5,
            "range": "± 0.5",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkDispatcherServeHTTP/unknown_command - allocs/op",
            "value": 30,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkDispatcherServeHTTP/unknown_command",
            "value": 3377,
            "range": "± 59.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkEmit - B/op",
            "value": 200,
            "unit": "B/op"
          },
          {
            "name": "BenchmarkEmit - allocs/op",
            "value": 4,
            "unit": "allocs/op"
          },
          {
            "name": "BenchmarkEmit",
            "value": 480.4,
            "unit": "ns/op"
          },
          {
            "name": "BenchmarkHandleAssistantChunk/long_chunk - B/op",
            "value": 13785089,
            "unit": "B/op"
          },
          {
            "name": "BenchmarkHandleAssistantChunk/long_chunk - allocs/op",
            "value": 25,
            "unit": "allocs/op"
          },
          {
            "name": "BenchmarkHandleAssistantChunk/long_chunk",
            "value": 1166288,
            "unit": "ns/op"
          },
          {
            "name": "BenchmarkHandleAssistantChunk/short_chunk - B/op",
            "value": 536606,
            "unit": "B/op"
          },
          {
            "name": "BenchmarkHandleAssistantChunk/short_chunk - allocs/op",
            "value": 23,
            "unit": "allocs/op"
          },
          {
            "name": "BenchmarkHandleAssistantChunk/short_chunk",
            "value": 61916,
            "unit": "ns/op"
          },
          {
            "name": "BenchmarkHandleAssistantChunk/with_reasoning - B/op",
            "value": 1135277,
            "unit": "B/op"
          },
          {
            "name": "BenchmarkHandleAssistantChunk/with_reasoning - allocs/op",
            "value": 20,
            "unit": "allocs/op"
          },
          {
            "name": "BenchmarkHandleAssistantChunk/with_reasoning",
            "value": 68049,
            "unit": "ns/op"
          },
          {
            "name": "BenchmarkHandleCommand/cache_hit - B/op",
            "value": 10255,
            "range": "± 0.5",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkHandleCommand/cache_hit - allocs/op",
            "value": 52,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkHandleCommand/cache_hit",
            "value": 10068,
            "range": "± 512",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkHandleCommand/cancel - B/op",
            "value": 7164.5,
            "range": "± 5",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkHandleCommand/cancel - allocs/op",
            "value": 37,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkHandleCommand/cancel",
            "value": 5128.5,
            "range": "± 138",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkHandleCommand/create_chat - B/op",
            "value": 10469,
            "range": "± 19.5",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkHandleCommand/create_chat - allocs/op",
            "value": 58,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkHandleCommand/create_chat",
            "value": 10673.5,
            "range": "± 526.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkHandleCommand/prompt - B/op",
            "value": 161204.5,
            "range": "± 21008.5",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkHandleCommand/prompt - allocs/op",
            "value": 141,
            "range": "± 13",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkHandleCommand/prompt",
            "value": 165414.5,
            "range": "± 21426.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkIgnoreMatcherMatches/rules_15_deep - B/op",
            "value": 32576,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkIgnoreMatcherMatches/rules_15_deep - allocs/op",
            "value": 131,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkIgnoreMatcherMatches/rules_15_deep",
            "value": 14473.5,
            "range": "± 121.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkIgnoreMatcherMatches/rules_15_medium - B/op",
            "value": 15888,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkIgnoreMatcherMatches/rules_15_medium - allocs/op",
            "value": 66,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkIgnoreMatcherMatches/rules_15_medium",
            "value": 9702.5,
            "range": "± 166.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkIgnoreMatcherMatches/rules_15_shallow - B/op",
            "value": 5360,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkIgnoreMatcherMatches/rules_15_shallow - allocs/op",
            "value": 25,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkIgnoreMatcherMatches/rules_15_shallow",
            "value": 5623,
            "range": "± 53.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkIgnoreMatcherMatches/rules_30_deep - B/op",
            "value": 66624,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkIgnoreMatcherMatches/rules_30_deep - allocs/op",
            "value": 264,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkIgnoreMatcherMatches/rules_30_deep",
            "value": 26015.5,
            "range": "± 453",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkIgnoreMatcherMatches/rules_30_medium - B/op",
            "value": 32016,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkIgnoreMatcherMatches/rules_30_medium - allocs/op",
            "value": 129,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkIgnoreMatcherMatches/rules_30_medium",
            "value": 15748.5,
            "range": "± 196.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkIgnoreMatcherMatches/rules_30_shallow - B/op",
            "value": 10224,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkIgnoreMatcherMatches/rules_30_shallow - allocs/op",
            "value": 44,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkIgnoreMatcherMatches/rules_30_shallow",
            "value": 7617,
            "range": "± 109.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkIgnoreMatcherMatches/rules_5_deep - B/op",
            "value": 12608,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkIgnoreMatcherMatches/rules_5_deep - allocs/op",
            "value": 53,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkIgnoreMatcherMatches/rules_5_deep",
            "value": 7738.5,
            "range": "± 46.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkIgnoreMatcherMatches/rules_5_medium - B/op",
            "value": 6416,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkIgnoreMatcherMatches/rules_5_medium - allocs/op",
            "value": 29,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkIgnoreMatcherMatches/rules_5_medium",
            "value": 6037,
            "range": "± 139",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkIgnoreMatcherMatches/rules_5_shallow - B/op",
            "value": 2544,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkIgnoreMatcherMatches/rules_5_shallow - allocs/op",
            "value": 14,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkIgnoreMatcherMatches/rules_5_shallow",
            "value": 4632,
            "range": "± 18.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkLineTrackerRecord/existing_1 - B/op",
            "value": 78,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkLineTrackerRecord/existing_1 - allocs/op",
            "value": 0,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkLineTrackerRecord/existing_1",
            "value": 69.78,
            "range": "± 1.455",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkLineTrackerRecord/existing_200 - B/op",
            "value": 78,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkLineTrackerRecord/existing_200 - allocs/op",
            "value": 0,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkLineTrackerRecord/existing_200",
            "value": 69.355,
            "range": "± 1.15",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkLineTrackerRecord/existing_50 - B/op",
            "value": 78,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkLineTrackerRecord/existing_50 - allocs/op",
            "value": 0,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkLineTrackerRecord/existing_50",
            "value": 69.635,
            "range": "± 0.795",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkLineTrackerRecordFromDiffs/diffs_1 - B/op",
            "value": 158,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkLineTrackerRecordFromDiffs/diffs_1 - allocs/op",
            "value": 2,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkLineTrackerRecordFromDiffs/diffs_1",
            "value": 138,
            "range": "± 7.65",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkLineTrackerRecordFromDiffs/diffs_10 - B/op",
            "value": 1580,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkLineTrackerRecordFromDiffs/diffs_10 - allocs/op",
            "value": 20,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkLineTrackerRecordFromDiffs/diffs_10",
            "value": 1509.5,
            "range": "± 70",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkLineTrackerRecordFromDiffs/diffs_50 - B/op",
            "value": 7903.5,
            "range": "± 2.5",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkLineTrackerRecordFromDiffs/diffs_50 - allocs/op",
            "value": 100,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkLineTrackerRecordFromDiffs/diffs_50",
            "value": 7885,
            "range": "± 189",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkMCPRegistrySnapshot/servers=1 - B/op",
            "value": 160,
            "unit": "B/op"
          },
          {
            "name": "BenchmarkMCPRegistrySnapshot/servers=1 - allocs/op",
            "value": 1,
            "unit": "allocs/op"
          },
          {
            "name": "BenchmarkMCPRegistrySnapshot/servers=1",
            "value": 94.21,
            "unit": "ns/op"
          },
          {
            "name": "BenchmarkMCPRegistrySnapshot/servers=20 - B/op",
            "value": 3456,
            "unit": "B/op"
          },
          {
            "name": "BenchmarkMCPRegistrySnapshot/servers=20 - allocs/op",
            "value": 1,
            "unit": "allocs/op"
          },
          {
            "name": "BenchmarkMCPRegistrySnapshot/servers=20",
            "value": 2434,
            "unit": "ns/op"
          },
          {
            "name": "BenchmarkMCPRegistrySnapshot/servers=5 - B/op",
            "value": 896,
            "unit": "B/op"
          },
          {
            "name": "BenchmarkMCPRegistrySnapshot/servers=5 - allocs/op",
            "value": 1,
            "unit": "allocs/op"
          },
          {
            "name": "BenchmarkMCPRegistrySnapshot/servers=5",
            "value": 371.2,
            "unit": "ns/op"
          },
          {
            "name": "BenchmarkNormaliseRegistryResponse/servers=20 - B/op",
            "value": 32190,
            "range": "± 3",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkNormaliseRegistryResponse/servers=20 - allocs/op",
            "value": 329,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkNormaliseRegistryResponse/servers=20",
            "value": 70261.5,
            "range": "± 478.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkNormaliseRegistryResponse/servers=5 - B/op",
            "value": 8022,
            "range": "± 0.5",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkNormaliseRegistryResponse/servers=5 - allocs/op",
            "value": 87,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkNormaliseRegistryResponse/servers=5",
            "value": 18140.5,
            "range": "± 332.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkNormaliseRegistryResponse/servers=50 - B/op",
            "value": 63758.5,
            "range": "± 7.5",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkNormaliseRegistryResponse/servers=50 - allocs/op",
            "value": 706,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkNormaliseRegistryResponse/servers=50",
            "value": 165760,
            "range": "± 2897",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkPushEncrypt - B/op",
            "value": 5736,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkPushEncrypt - allocs/op",
            "value": 60,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkPushEncrypt",
            "value": 68976,
            "range": "± 805",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkReadLoop_Notifications - B/op",
            "value": 66316,
            "range": "± 4.5",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkReadLoop_Notifications - allocs/op",
            "value": 7,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkReadLoop_Notifications",
            "value": 9264,
            "range": "± 689.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkReadLoop_Responses/large - B/op",
            "value": 7605456.5,
            "range": "± 240",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkReadLoop_Responses/large - allocs/op",
            "value": 307,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkReadLoop_Responses/large",
            "value": 7299966,
            "range": "± 103330.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkReadLoop_Responses/medium - B/op",
            "value": 567529,
            "range": "± 21",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkReadLoop_Responses/medium - allocs/op",
            "value": 304,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkReadLoop_Responses/medium",
            "value": 526682.5,
            "range": "± 9481",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkReadLoop_Responses/small - B/op",
            "value": 84212.5,
            "range": "± 5.5",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkReadLoop_Responses/small - allocs/op",
            "value": 304,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkReadLoop_Responses/small",
            "value": 70120.5,
            "range": "± 484.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkRegistryCacheGetOrFetch/hit_same_key - B/op",
            "value": 48,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkRegistryCacheGetOrFetch/hit_same_key - allocs/op",
            "value": 1,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkRegistryCacheGetOrFetch/hit_same_key",
            "value": 82.165,
            "range": "± 2.785",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkRegistryCacheGetOrFetch/miss_different_keys - B/op",
            "value": 463,
            "range": "± 0.5",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkRegistryCacheGetOrFetch/miss_different_keys - allocs/op",
            "value": 10,
            "range": "± 0.5",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkRegistryCacheGetOrFetch/miss_different_keys",
            "value": 6386.5,
            "range": "± 45.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkRegistryCacheGetOrFetch/miss_same_key_singleflight - B/op",
            "value": 48,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkRegistryCacheGetOrFetch/miss_same_key_singleflight - allocs/op",
            "value": 1,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkRegistryCacheGetOrFetch/miss_same_key_singleflight",
            "value": 81.915,
            "range": "± 1.38",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkResolveInsideAbs/absolute_inside - B/op",
            "value": 2624,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkResolveInsideAbs/absolute_inside - allocs/op",
            "value": 32,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkResolveInsideAbs/absolute_inside",
            "value": 14519.5,
            "range": "± 281",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkResolveInsideAbs/missing_parent - B/op",
            "value": 2704,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkResolveInsideAbs/missing_parent - allocs/op",
            "value": 33,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkResolveInsideAbs/missing_parent",
            "value": 14659.5,
            "range": "± 210.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkResolveInsideAbs/relative - B/op",
            "value": 2656,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkResolveInsideAbs/relative - allocs/op",
            "value": 33,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkResolveInsideAbs/relative",
            "value": 14623,
            "range": "± 136",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkResolvePath/allowed_path - B/op",
            "value": 2336,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkResolvePath/allowed_path - allocs/op",
            "value": 28,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkResolvePath/allowed_path",
            "value": 12304,
            "range": "± 38",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkResolvePath/deep_nested_path - B/op",
            "value": 2352,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkResolvePath/deep_nested_path - allocs/op",
            "value": 28,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkResolvePath/deep_nested_path",
            "value": 12312,
            "range": "± 34.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkResolvePath/outside_roots_path - B/op",
            "value": 40,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkResolvePath/outside_roots_path - allocs/op",
            "value": 2,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkResolvePath/outside_roots_path",
            "value": 224.8,
            "range": "± 1.7",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkResolvePath/symlink_path - B/op",
            "value": 3856,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkResolvePath/symlink_path - allocs/op",
            "value": 47,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkResolvePath/symlink_path",
            "value": 20048.5,
            "range": "± 236",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSanitizeOutput/ansi_heavy - B/op",
            "value": 15693.5,
            "range": "± 27",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSanitizeOutput/ansi_heavy - allocs/op",
            "value": 18,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSanitizeOutput/ansi_heavy",
            "value": 207737,
            "range": "± 2994.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSanitizeOutput/clean_short - B/op",
            "value": 241,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSanitizeOutput/clean_short - allocs/op",
            "value": 3,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSanitizeOutput/clean_short",
            "value": 362.4,
            "range": "± 3.7",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSanitizeOutput/mixed_large - B/op",
            "value": 84538.5,
            "range": "± 30.5",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSanitizeOutput/mixed_large - allocs/op",
            "value": 20,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSanitizeOutput/mixed_large",
            "value": 85311,
            "range": "± 495.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSanitizeOutput/unicode_heavy - B/op",
            "value": 18844,
            "range": "± 12.5",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSanitizeOutput/unicode_heavy - allocs/op",
            "value": 7,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSanitizeOutput/unicode_heavy",
            "value": 17953,
            "range": "± 147",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkScanKiroDirFS/20_docs - B/op",
            "value": 30240,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkScanKiroDirFS/20_docs - allocs/op",
            "value": 210,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkScanKiroDirFS/20_docs",
            "value": 22348,
            "range": "± 315.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkScanKiroDirFS/50_docs - B/op",
            "value": 32664,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkScanKiroDirFS/50_docs - allocs/op",
            "value": 211,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkScanKiroDirFS/50_docs",
            "value": 26547.5,
            "range": "± 269",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkScanKiroDirFS/5_docs - B/op",
            "value": 7688,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkScanKiroDirFS/5_docs - allocs/op",
            "value": 71,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkScanKiroDirFS/5_docs",
            "value": 6494.5,
            "range": "± 76.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkScrubAuth/chained_16 - B/op",
            "value": 233,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkScrubAuth/chained_16 - allocs/op",
            "value": 14,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkScrubAuth/chained_16",
            "value": 1357.5,
            "range": "± 16",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkScrubAuth/clean - B/op",
            "value": 529,
            "range": "± 0.5",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkScrubAuth/clean - allocs/op",
            "value": 9,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkScrubAuth/clean",
            "value": 3257.5,
            "range": "± 178",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkScrubAuth/long_output_1KB - B/op",
            "value": 6976.5,
            "range": "± 4.5",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkScrubAuth/long_output_1KB - allocs/op",
            "value": 9,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkScrubAuth/long_output_1KB",
            "value": 43536,
            "range": "± 596.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkScrubAuth/single_userinfo - B/op",
            "value": 498,
            "range": "± 0.5",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkScrubAuth/single_userinfo - allocs/op",
            "value": 14,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkScrubAuth/single_userinfo",
            "value": 3144.5,
            "range": "± 90.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSecurityMiddleware/GET_headers_only - B/op",
            "value": 66,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSecurityMiddleware/GET_headers_only - allocs/op",
            "value": 5,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSecurityMiddleware/GET_headers_only",
            "value": 218.95,
            "range": "± 5.7",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSecurityMiddleware/POST_same_origin - B/op",
            "value": 210,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSecurityMiddleware/POST_same_origin - allocs/op",
            "value": 6,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSecurityMiddleware/POST_same_origin",
            "value": 394.6,
            "range": "± 1.9",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkStore_AppendMessage - B/op",
            "value": 14489075,
            "unit": "B/op"
          },
          {
            "name": "BenchmarkStore_AppendMessage - allocs/op",
            "value": 4145,
            "unit": "allocs/op"
          },
          {
            "name": "BenchmarkStore_AppendMessage",
            "value": 8657991,
            "unit": "ns/op"
          },
          {
            "name": "BenchmarkTranslateACPEvent/agent_message_chunk - B/op",
            "value": 1326339,
            "unit": "B/op"
          },
          {
            "name": "BenchmarkTranslateACPEvent/agent_message_chunk - allocs/op",
            "value": 28,
            "unit": "allocs/op"
          },
          {
            "name": "BenchmarkTranslateACPEvent/agent_message_chunk",
            "value": 86220,
            "unit": "ns/op"
          },
          {
            "name": "BenchmarkTranslateACPEvent/tool_call - B/op",
            "value": 5000,
            "unit": "B/op"
          },
          {
            "name": "BenchmarkTranslateACPEvent/tool_call - allocs/op",
            "value": 35,
            "unit": "allocs/op"
          },
          {
            "name": "BenchmarkTranslateACPEvent/tool_call",
            "value": 7828,
            "unit": "ns/op"
          },
          {
            "name": "BenchmarkTranslateACPEvent/tool_call_update - B/op",
            "value": 976,
            "unit": "B/op"
          },
          {
            "name": "BenchmarkTranslateACPEvent/tool_call_update - allocs/op",
            "value": 12,
            "unit": "allocs/op"
          },
          {
            "name": "BenchmarkTranslateACPEvent/tool_call_update",
            "value": 3608,
            "unit": "ns/op"
          },
          {
            "name": "BenchmarkTranslator_FullTurn - B/op",
            "value": 180367,
            "range": "± 972",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkTranslator_FullTurn - allocs/op",
            "value": 1553,
            "range": "± 2",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkTranslator_FullTurn",
            "value": 167171,
            "range": "± 5172.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkTranslator_HandleAssistantChunk - B/op",
            "value": 592729,
            "range": "± 107372",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkTranslator_HandleAssistantChunk - allocs/op",
            "value": 13,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkTranslator_HandleAssistantChunk",
            "value": 87901,
            "range": "± 12301",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkTranslator_HandleToolCall - B/op",
            "value": 3231.5,
            "range": "± 97",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkTranslator_HandleToolCall - allocs/op",
            "value": 17,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkTranslator_HandleToolCall",
            "value": 3158.5,
            "range": "± 215.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkTranslator_HandleUsageUpdate - B/op",
            "value": 56,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkTranslator_HandleUsageUpdate - allocs/op",
            "value": 2,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkTranslator_HandleUsageUpdate",
            "value": 307.35,
            "range": "± 5.6",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkUtilityBridge_DrainResponse/chunks=100 - B/op",
            "value": 5000,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkUtilityBridge_DrainResponse/chunks=100 - allocs/op",
            "value": 114,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkUtilityBridge_DrainResponse/chunks=100",
            "value": 67388.5,
            "range": "± 496.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkUtilityBridge_DrainResponse/chunks=1000 - B/op",
            "value": 33992,
            "range": "± 0.5",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkUtilityBridge_DrainResponse/chunks=1000 - allocs/op",
            "value": 1014,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkUtilityBridge_DrainResponse/chunks=1000",
            "value": 653164.5,
            "range": "± 4680.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkUtilityBridge_DrainResponse/chunks=20 - B/op",
            "value": 2248,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkUtilityBridge_DrainResponse/chunks=20 - allocs/op",
            "value": 34,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkUtilityBridge_DrainResponse/chunks=20",
            "value": 14813,
            "range": "± 264.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkUtilityBridge_DrainResponse/chunks=5 - B/op",
            "value": 1768,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkUtilityBridge_DrainResponse/chunks=5 - allocs/op",
            "value": 19,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkUtilityBridge_DrainResponse/chunks=5",
            "value": 4773,
            "range": "± 87",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkUtilityBridge_DrainResponse/chunks=500 - B/op",
            "value": 17800,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkUtilityBridge_DrainResponse/chunks=500 - allocs/op",
            "value": 514,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkUtilityBridge_DrainResponse/chunks=500",
            "value": 330094,
            "range": "± 2374.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          }
        ]
      },
      {
        "commit": {
          "author": {
            "name": "Christopher Plieger",
            "username": "cplieger",
            "email": "917744+cplieger@users.noreply.github.com"
          },
          "committer": {
            "name": "GitHub",
            "username": "web-flow",
            "email": "noreply@github.com"
          },
          "id": "e9fbabb1dd7e2a15de9212889c454856c6b43e0f",
          "message": "ci: scale gremlins timeouts by workers, pick integration by cost (#705)\n\nci: scale gremlins timeouts by workers, pick integration by cost\n\ngremlins gives each mutant coverage-pass time x a coefficient (default 3), but the coverage pass runs alone on every CPU while each of N workers gets about 1/N of them, so a CPU-bound suite timed out surviving mutants and scored them out of efficacy (wiregen read 100% with 12 mutants alive). The coefficient is now 3 x the workers passed to gremlins.\n\nEach module now runs --integration --coverpkg ./... when the measured suite time x the dry-run mutant count / workers is under a quarter of the run job time cap, which lives once as a fanout output; otherwise, or when any input cannot be measured, it stays per-package. The tracker row records the mode, marks the week a repo switches, and compares trend and the regression label only against weeks of the same mode. The gremlins tracker goldens are regenerated on purpose for the new Mode column and a corrected efficacy legend (TIMED OUT counts in neither side).",
          "timestamp": "2026-10-06T21:29:40Z",
          "url": "https://github.com/cplieger/ci/commit/e9fbabb1dd7e2a15de9212889c454856c6b43e0f"
        },
        "date": 1791339264746,
        "tool": "customSmallerIsBetter",
        "benches": [
          {
            "name": "BenchmarkBridgeManagerGetOrInsert/create - B/op",
            "value": 877,
            "range": "± 14",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkBridgeManagerGetOrInsert/create - allocs/op",
            "value": 6,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkBridgeManagerGetOrInsert/create",
            "value": 1120.5,
            "range": "± 227.85",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkBridgeManagerGetOrInsert/exists - B/op",
            "value": 8,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkBridgeManagerGetOrInsert/exists - allocs/op",
            "value": 1,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkBridgeManagerGetOrInsert/exists",
            "value": 100.8,
            "range": "± 3.47",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkBridgeReadLoop - B/op",
            "value": 144931,
            "range": "± 97",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkBridgeReadLoop - allocs/op",
            "value": 2753,
            "range": "± 1",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkBridgeReadLoop",
            "value": 908819,
            "range": "± 14042.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkBridgeRespond - B/op",
            "value": 752,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkBridgeRespond - allocs/op",
            "value": 9,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkBridgeRespond",
            "value": 6154,
            "range": "± 343",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=262144/write=1024 - B/op",
            "value": 0,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=262144/write=1024 - allocs/op",
            "value": 0,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=262144/write=1024",
            "value": 20.12,
            "range": "± 0.215",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=262144/write=64 - B/op",
            "value": 0,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=262144/write=64 - allocs/op",
            "value": 0,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=262144/write=64",
            "value": 8.6265,
            "range": "± 0.0445",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=262144/write=8192 - B/op",
            "value": 0,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=262144/write=8192 - allocs/op",
            "value": 0,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=262144/write=8192",
            "value": 129.65,
            "range": "± 4.1",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=4096/write=1024 - B/op",
            "value": 0,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=4096/write=1024 - allocs/op",
            "value": 0,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=4096/write=1024",
            "value": 16.285,
            "range": "± 1.53",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=4096/write=64 - B/op",
            "value": 0,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=4096/write=64 - allocs/op",
            "value": 0,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=4096/write=64",
            "value": 8.6035,
            "range": "± 3.822",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=4096/write=8192 - B/op",
            "value": 0,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=4096/write=8192 - allocs/op",
            "value": 0,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=4096/write=8192",
            "value": 43.2,
            "range": "± 1.315",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=65536/write=1024 - B/op",
            "value": 0,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=65536/write=1024 - allocs/op",
            "value": 0,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=65536/write=1024",
            "value": 20.11,
            "range": "± 0.03",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=65536/write=64 - B/op",
            "value": 0,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=65536/write=64 - allocs/op",
            "value": 0,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=65536/write=64",
            "value": 8.66,
            "range": "± 0.23",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=65536/write=8192 - B/op",
            "value": 0,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=65536/write=8192 - allocs/op",
            "value": 0,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkByteRing_Write/cap=65536/write=8192",
            "value": 123,
            "range": "± 0.45",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkCensusMeta - B/op",
            "value": 552,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkCensusMeta - allocs/op",
            "value": 11,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkCensusMeta",
            "value": 2039.5,
            "range": "± 6",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkCheapestModel/catalog_100 - B/op",
            "value": 0,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkCheapestModel/catalog_100 - allocs/op",
            "value": 0,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkCheapestModel/catalog_100",
            "value": 1915,
            "range": "± 2.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkCheapestModel/catalog_20 - B/op",
            "value": 0,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkCheapestModel/catalog_20 - allocs/op",
            "value": 0,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkCheapestModel/catalog_20",
            "value": 389.65,
            "range": "± 0.75",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkCheapestModel/catalog_5 - B/op",
            "value": 0,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkCheapestModel/catalog_5 - allocs/op",
            "value": 0,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkCheapestModel/catalog_5",
            "value": 98.79,
            "range": "± 1.575",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkDispatcherServeHTTP/dispatch - B/op",
            "value": 6836,
            "range": "± 0.5",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkDispatcherServeHTTP/dispatch - allocs/op",
            "value": 30,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkDispatcherServeHTTP/dispatch",
            "value": 4708.5,
            "range": "± 258.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkDispatcherServeHTTP/unknown_command - B/op",
            "value": 6933,
            "range": "± 0.5",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkDispatcherServeHTTP/unknown_command - allocs/op",
            "value": 30,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkDispatcherServeHTTP/unknown_command",
            "value": 4664.5,
            "range": "± 31",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkEmit - B/op",
            "value": 200,
            "unit": "B/op"
          },
          {
            "name": "BenchmarkEmit - allocs/op",
            "value": 4,
            "unit": "allocs/op"
          },
          {
            "name": "BenchmarkEmit",
            "value": 684.5,
            "unit": "ns/op"
          },
          {
            "name": "BenchmarkHandleAssistantChunk/long_chunk - B/op",
            "value": 20502686,
            "unit": "B/op"
          },
          {
            "name": "BenchmarkHandleAssistantChunk/long_chunk - allocs/op",
            "value": 14,
            "unit": "allocs/op"
          },
          {
            "name": "BenchmarkHandleAssistantChunk/long_chunk",
            "value": 2226793,
            "unit": "ns/op"
          },
          {
            "name": "BenchmarkHandleAssistantChunk/short_chunk - B/op",
            "value": 455795,
            "unit": "B/op"
          },
          {
            "name": "BenchmarkHandleAssistantChunk/short_chunk - allocs/op",
            "value": 9,
            "unit": "allocs/op"
          },
          {
            "name": "BenchmarkHandleAssistantChunk/short_chunk",
            "value": 73085,
            "unit": "ns/op"
          },
          {
            "name": "BenchmarkHandleAssistantChunk/with_reasoning - B/op",
            "value": 905833,
            "unit": "B/op"
          },
          {
            "name": "BenchmarkHandleAssistantChunk/with_reasoning - allocs/op",
            "value": 9,
            "unit": "allocs/op"
          },
          {
            "name": "BenchmarkHandleAssistantChunk/with_reasoning",
            "value": 62779,
            "unit": "ns/op"
          },
          {
            "name": "BenchmarkHandleCommand/cache_hit - B/op",
            "value": 12746,
            "range": "± 2",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkHandleCommand/cache_hit - allocs/op",
            "value": 78,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkHandleCommand/cache_hit",
            "value": 35681.5,
            "range": "± 978",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkHandleCommand/cancel - B/op",
            "value": 7180.5,
            "range": "± 5",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkHandleCommand/cancel - allocs/op",
            "value": 38,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkHandleCommand/cancel",
            "value": 7288,
            "range": "± 332.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkHandleCommand/create_chat - B/op",
            "value": 13022.5,
            "range": "± 4",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkHandleCommand/create_chat - allocs/op",
            "value": 84,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkHandleCommand/create_chat",
            "value": 37881.5,
            "range": "± 2687.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkHandleCommand/prompt - B/op",
            "value": 53425.5,
            "range": "± 1842.5",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkHandleCommand/prompt - allocs/op",
            "value": 56,
            "range": "± 0.5",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkHandleCommand/prompt",
            "value": 18174,
            "range": "± 1015",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkLineTrackerRecord/existing_1 - B/op",
            "value": 78,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkLineTrackerRecord/existing_1 - allocs/op",
            "value": 0,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkLineTrackerRecord/existing_1",
            "value": 94.975,
            "range": "± 0.51",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkLineTrackerRecord/existing_200 - B/op",
            "value": 78,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkLineTrackerRecord/existing_200 - allocs/op",
            "value": 0,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkLineTrackerRecord/existing_200",
            "value": 94.635,
            "range": "± 0.56",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkLineTrackerRecord/existing_50 - B/op",
            "value": 78,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkLineTrackerRecord/existing_50 - allocs/op",
            "value": 0,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkLineTrackerRecord/existing_50",
            "value": 94.805,
            "range": "± 0.78",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkLineTrackerRecordFromDiffs/diffs_1 - B/op",
            "value": 158,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkLineTrackerRecordFromDiffs/diffs_1 - allocs/op",
            "value": 2,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkLineTrackerRecordFromDiffs/diffs_1",
            "value": 201.15,
            "range": "± 11.95",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkLineTrackerRecordFromDiffs/diffs_10 - B/op",
            "value": 1580,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkLineTrackerRecordFromDiffs/diffs_10 - allocs/op",
            "value": 20,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkLineTrackerRecordFromDiffs/diffs_10",
            "value": 2083.5,
            "range": "± 51.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkLineTrackerRecordFromDiffs/diffs_50 - B/op",
            "value": 7904,
            "range": "± 3.5",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkLineTrackerRecordFromDiffs/diffs_50 - allocs/op",
            "value": 100,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkLineTrackerRecordFromDiffs/diffs_50",
            "value": 10731.5,
            "range": "± 201.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkMCPRegistrySnapshot/servers=1 - B/op",
            "value": 224,
            "unit": "B/op"
          },
          {
            "name": "BenchmarkMCPRegistrySnapshot/servers=1 - allocs/op",
            "value": 1,
            "unit": "allocs/op"
          },
          {
            "name": "BenchmarkMCPRegistrySnapshot/servers=1",
            "value": 135.6,
            "unit": "ns/op"
          },
          {
            "name": "BenchmarkMCPRegistrySnapshot/servers=20 - B/op",
            "value": 4864,
            "unit": "B/op"
          },
          {
            "name": "BenchmarkMCPRegistrySnapshot/servers=20 - allocs/op",
            "value": 1,
            "unit": "allocs/op"
          },
          {
            "name": "BenchmarkMCPRegistrySnapshot/servers=20",
            "value": 4172,
            "unit": "ns/op"
          },
          {
            "name": "BenchmarkMCPRegistrySnapshot/servers=5 - B/op",
            "value": 1152,
            "unit": "B/op"
          },
          {
            "name": "BenchmarkMCPRegistrySnapshot/servers=5 - allocs/op",
            "value": 1,
            "unit": "allocs/op"
          },
          {
            "name": "BenchmarkMCPRegistrySnapshot/servers=5",
            "value": 562.8,
            "unit": "ns/op"
          },
          {
            "name": "BenchmarkNormaliseRegistryResponse/servers=20 - B/op",
            "value": 32187.5,
            "range": "± 3.5",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkNormaliseRegistryResponse/servers=20 - allocs/op",
            "value": 329,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkNormaliseRegistryResponse/servers=20",
            "value": 97695.5,
            "range": "± 2624.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkNormaliseRegistryResponse/servers=5 - B/op",
            "value": 8022,
            "range": "± 2",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkNormaliseRegistryResponse/servers=5 - allocs/op",
            "value": 87,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkNormaliseRegistryResponse/servers=5",
            "value": 25085,
            "range": "± 1888.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkNormaliseRegistryResponse/servers=50 - B/op",
            "value": 63765,
            "range": "± 6",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkNormaliseRegistryResponse/servers=50 - allocs/op",
            "value": 706,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkNormaliseRegistryResponse/servers=50",
            "value": 227911.5,
            "range": "± 946",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkPushEncrypt - B/op",
            "value": 5736,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkPushEncrypt - allocs/op",
            "value": 60,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkPushEncrypt",
            "value": 80884.5,
            "range": "± 203",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkReadLoop_Notifications - B/op",
            "value": 66313.5,
            "range": "± 4.5",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkReadLoop_Notifications - allocs/op",
            "value": 7,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkReadLoop_Notifications",
            "value": 11790,
            "range": "± 1896",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkReadLoop_Responses/large - B/op",
            "value": 7605369.5,
            "range": "± 214",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkReadLoop_Responses/large - allocs/op",
            "value": 307,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkReadLoop_Responses/large",
            "value": 9907049,
            "range": "± 106251",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkReadLoop_Responses/medium - B/op",
            "value": 567518.5,
            "range": "± 16",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkReadLoop_Responses/medium - allocs/op",
            "value": 304,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkReadLoop_Responses/medium",
            "value": 729142,
            "range": "± 7726",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkReadLoop_Responses/small - B/op",
            "value": 84214.5,
            "range": "± 5.5",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkReadLoop_Responses/small - allocs/op",
            "value": 304,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkReadLoop_Responses/small",
            "value": 105874.5,
            "range": "± 3408",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkRegistryCacheGetOrFetch/hit_same_key - B/op",
            "value": 48,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkRegistryCacheGetOrFetch/hit_same_key - allocs/op",
            "value": 1,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkRegistryCacheGetOrFetch/hit_same_key",
            "value": 103.35,
            "range": "± 3.85",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkRegistryCacheGetOrFetch/miss_different_keys - B/op",
            "value": 463,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkRegistryCacheGetOrFetch/miss_different_keys - allocs/op",
            "value": 9,
            "range": "± 0.5",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkRegistryCacheGetOrFetch/miss_different_keys",
            "value": 8363.5,
            "range": "± 51.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkRegistryCacheGetOrFetch/miss_same_key_singleflight - B/op",
            "value": 48,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkRegistryCacheGetOrFetch/miss_same_key_singleflight - allocs/op",
            "value": 1,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkRegistryCacheGetOrFetch/miss_same_key_singleflight",
            "value": 103.25,
            "range": "± 1.8",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkResolveInsideAbs/absolute_inside - B/op",
            "value": 2624,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkResolveInsideAbs/absolute_inside - allocs/op",
            "value": 32,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkResolveInsideAbs/absolute_inside",
            "value": 15355,
            "range": "± 599",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkResolveInsideAbs/missing_parent - B/op",
            "value": 2704,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkResolveInsideAbs/missing_parent - allocs/op",
            "value": 33,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkResolveInsideAbs/missing_parent",
            "value": 15716,
            "range": "± 448.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkResolveInsideAbs/relative - B/op",
            "value": 2656,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkResolveInsideAbs/relative - allocs/op",
            "value": 33,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkResolveInsideAbs/relative",
            "value": 15386,
            "range": "± 247.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkResolvePath/allowed_path - B/op",
            "value": 3312,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkResolvePath/allowed_path - allocs/op",
            "value": 56,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkResolvePath/allowed_path",
            "value": 15597,
            "range": "± 199",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkResolvePath/deep_nested_path - B/op",
            "value": 3328,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkResolvePath/deep_nested_path - allocs/op",
            "value": 56,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkResolvePath/deep_nested_path",
            "value": 15641.5,
            "range": "± 555.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkResolvePath/outside_roots_path - B/op",
            "value": 40,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkResolvePath/outside_roots_path - allocs/op",
            "value": 2,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkResolvePath/outside_roots_path",
            "value": 292.35,
            "range": "± 15.1",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkResolvePath/symlink_path - B/op",
            "value": 4832,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkResolvePath/symlink_path - allocs/op",
            "value": 75,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkResolvePath/symlink_path",
            "value": 23910.5,
            "range": "± 164",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSanitizeOutput/ansi_heavy - B/op",
            "value": 15712.5,
            "range": "± 18",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSanitizeOutput/ansi_heavy - allocs/op",
            "value": 18,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSanitizeOutput/ansi_heavy",
            "value": 267994.5,
            "range": "± 4620",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSanitizeOutput/clean_short - B/op",
            "value": 241,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSanitizeOutput/clean_short - allocs/op",
            "value": 3,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSanitizeOutput/clean_short",
            "value": 502.8,
            "range": "± 9.25",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSanitizeOutput/mixed_large - B/op",
            "value": 84518.5,
            "range": "± 66.5",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSanitizeOutput/mixed_large - allocs/op",
            "value": 20,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSanitizeOutput/mixed_large",
            "value": 109553,
            "range": "± 520.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSanitizeOutput/unicode_heavy - B/op",
            "value": 18849,
            "range": "± 11",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSanitizeOutput/unicode_heavy - allocs/op",
            "value": 7,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSanitizeOutput/unicode_heavy",
            "value": 23770.5,
            "range": "± 229",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkScanKiroDirFS/20_docs - B/op",
            "value": 30240,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkScanKiroDirFS/20_docs - allocs/op",
            "value": 210,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkScanKiroDirFS/20_docs",
            "value": 29910,
            "range": "± 790.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkScanKiroDirFS/50_docs - B/op",
            "value": 32664,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkScanKiroDirFS/50_docs - allocs/op",
            "value": 211,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkScanKiroDirFS/50_docs",
            "value": 35568.5,
            "range": "± 196.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkScanKiroDirFS/5_docs - B/op",
            "value": 7688,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkScanKiroDirFS/5_docs - allocs/op",
            "value": 71,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkScanKiroDirFS/5_docs",
            "value": 8758,
            "range": "± 676.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkScrubAuth/chained_16 - B/op",
            "value": 233,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkScrubAuth/chained_16 - allocs/op",
            "value": 14,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkScrubAuth/chained_16",
            "value": 1849.5,
            "range": "± 119.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkScrubAuth/clean - B/op",
            "value": 529,
            "range": "± 0.5",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkScrubAuth/clean - allocs/op",
            "value": 9,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkScrubAuth/clean",
            "value": 4554.5,
            "range": "± 88",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkScrubAuth/long_output_1KB - B/op",
            "value": 6969,
            "range": "± 7.5",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkScrubAuth/long_output_1KB - allocs/op",
            "value": 9,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkScrubAuth/long_output_1KB",
            "value": 59503,
            "range": "± 537",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkScrubAuth/single_userinfo - B/op",
            "value": 498,
            "range": "± 0.5",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkScrubAuth/single_userinfo - allocs/op",
            "value": 14,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkScrubAuth/single_userinfo",
            "value": 4301.5,
            "range": "± 68.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSecurityMiddleware/GET_headers_only - B/op",
            "value": 66,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSecurityMiddleware/GET_headers_only - allocs/op",
            "value": 5,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSecurityMiddleware/GET_headers_only",
            "value": 288.65,
            "range": "± 6.3",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSecurityMiddleware/POST_same_origin - B/op",
            "value": 210,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSecurityMiddleware/POST_same_origin - allocs/op",
            "value": 6,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkSecurityMiddleware/POST_same_origin",
            "value": 509.55,
            "range": "± 3.05",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkStore_Append - B/op",
            "value": 1873,
            "unit": "B/op"
          },
          {
            "name": "BenchmarkStore_Append - allocs/op",
            "value": 14,
            "unit": "allocs/op"
          },
          {
            "name": "BenchmarkStore_Append",
            "value": 8039,
            "unit": "ns/op"
          },
          {
            "name": "BenchmarkTranslateACPEvent/agent_message_chunk - B/op",
            "value": 1125235,
            "unit": "B/op"
          },
          {
            "name": "BenchmarkTranslateACPEvent/agent_message_chunk - allocs/op",
            "value": 14,
            "unit": "allocs/op"
          },
          {
            "name": "BenchmarkTranslateACPEvent/agent_message_chunk",
            "value": 77711,
            "unit": "ns/op"
          },
          {
            "name": "BenchmarkTranslateACPEvent/tool_call - B/op",
            "value": 3794,
            "unit": "B/op"
          },
          {
            "name": "BenchmarkTranslateACPEvent/tool_call - allocs/op",
            "value": 38,
            "unit": "allocs/op"
          },
          {
            "name": "BenchmarkTranslateACPEvent/tool_call",
            "value": 19078,
            "unit": "ns/op"
          },
          {
            "name": "BenchmarkTranslateACPEvent/tool_call_update - B/op",
            "value": 1040,
            "unit": "B/op"
          },
          {
            "name": "BenchmarkTranslateACPEvent/tool_call_update - allocs/op",
            "value": 13,
            "unit": "allocs/op"
          },
          {
            "name": "BenchmarkTranslateACPEvent/tool_call_update",
            "value": 4847,
            "unit": "ns/op"
          },
          {
            "name": "BenchmarkTranslator_FullTurn - B/op",
            "value": 180776,
            "range": "± 868",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkTranslator_FullTurn - allocs/op",
            "value": 347,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkTranslator_FullTurn",
            "value": 209057.5,
            "range": "± 7884.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkTranslator_HandleAssistantChunk - B/op",
            "value": 447773.5,
            "range": "± 70295",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkTranslator_HandleAssistantChunk - allocs/op",
            "value": 4,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkTranslator_HandleAssistantChunk",
            "value": 106081.5,
            "range": "± 4502.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkTranslator_HandleToolCall - B/op",
            "value": 2097,
            "range": "± 30",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkTranslator_HandleToolCall - allocs/op",
            "value": 12,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkTranslator_HandleToolCall",
            "value": 4666.5,
            "range": "± 303.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkTranslator_HandleUsageUpdate - B/op",
            "value": 56,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkTranslator_HandleUsageUpdate - allocs/op",
            "value": 2,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkTranslator_HandleUsageUpdate",
            "value": 475.55,
            "range": "± 1.95",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkUtilityBridge_DrainResponse/chunks=100 - B/op",
            "value": 5208,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkUtilityBridge_DrainResponse/chunks=100 - allocs/op",
            "value": 114,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkUtilityBridge_DrainResponse/chunks=100",
            "value": 90196.5,
            "range": "± 148",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkUtilityBridge_DrainResponse/chunks=1000 - B/op",
            "value": 34200,
            "range": "± 0.5",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkUtilityBridge_DrainResponse/chunks=1000 - allocs/op",
            "value": 1014,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkUtilityBridge_DrainResponse/chunks=1000",
            "value": 887293,
            "range": "± 2024",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkUtilityBridge_DrainResponse/chunks=20 - B/op",
            "value": 2456,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkUtilityBridge_DrainResponse/chunks=20 - allocs/op",
            "value": 34,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkUtilityBridge_DrainResponse/chunks=20",
            "value": 19291.5,
            "range": "± 311.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkUtilityBridge_DrainResponse/chunks=5 - B/op",
            "value": 1976,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkUtilityBridge_DrainResponse/chunks=5 - allocs/op",
            "value": 19,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkUtilityBridge_DrainResponse/chunks=5",
            "value": 6343,
            "range": "± 382.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkUtilityBridge_DrainResponse/chunks=500 - B/op",
            "value": 18008,
            "range": "± 0",
            "unit": "B/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkUtilityBridge_DrainResponse/chunks=500 - allocs/op",
            "value": 514,
            "range": "± 0",
            "unit": "allocs/op",
            "extra": "10 samples, median"
          },
          {
            "name": "BenchmarkUtilityBridge_DrainResponse/chunks=500",
            "value": 444526.5,
            "range": "± 895.5",
            "unit": "ns/op",
            "extra": "10 samples, median"
          }
        ]
      }
    ]
  }
}