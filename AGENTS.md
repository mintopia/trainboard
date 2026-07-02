# Train Departure Display

## Concept

This is a Pi Zero W 2 application with an SSD1322-based 256x64 SPI OLED. The aim is to
replicate a typical train departure board in the UK. Similar to the Desktop Departures
board at https://ukdepartureboards.co.uk/

## Current State

This is heavily modified from the original project, with faster animations; however it
uses an external API for fetching prepared train data and configuration.

## Planned Features

 - Ground up rewrite
 - Faster bootup times
 - Better OLED driving
 - Built in configuration Web UI
 - Access Point Mode for Initial Configuration
 - Automatic use and configuration of OTG Network adapters
 - MDns discovery on LAN
 - Firmware update from GitHub releases
 - Direct use of the RealTimeTrains API

## Guidance

 - Use expert subagents for implementation, use the best model for the task.
 - Use Opus or equivalent for planning

## Project Management

 - Use GitHub API to manage a project for this repository
 - Track milestones and tasks

## Code Quality

 - Red/Green TDD
 - Linting of all code to acceptable standards
 - Unit tests of all functionality
 - Strict gateway of linting and tests before pushing code
 - Review of all finished work at end of milestones by Codex
