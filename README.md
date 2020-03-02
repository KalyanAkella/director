# director
A simple reverse proxy that forwards HTTP requests to multiple backends
for purposes of testing and benchmarking changes to existing services.

Basically, director just forwards the incoming HTTP request to a given
'primary' endpoint while in the background replays that request to multiple
'secondary' endpoints.

Primarily this enables the usecase for code changes to existing services
be deployed to new endpoints without affecting the production traffic
due to bugs/performance issues with the new code base.

It also reports the following metrics for every HTTP request handled and
for both primary and secondary endpoints:
1. Response times
2. Number of successes
3. Number of failures (only network failures)

## Getting started
The easiest way to get director is to use one of the pre-built release binaries
which are available for OSX and Linux, from the [release page](https://github.com/KalyanAkella/director/releases).

Upon downloading to a certain folder, director can be launched as:

```bash
$ director -configFile <path_to_config_yml_file>
```

A sample configuration file (sample_config.yml) is included in the root of this repo.

## Building
If you instead prefer to build director locally, here are the steps:
1. Ensure you have GoLang version 1.13+ installed
2. Checkout the repo
3. Execute `make build` or `make test build`
4. Launch director from `./bin/director -configFile <path_to_config_yml_file>`
