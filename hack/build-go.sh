#!/bin/bash

# Name of your Go source file without the .go extension
APP_NAME="robocni"

# Ensure Go is installed
if ! command -v go &> /dev/null
then
    echo "Go could not be found. Please install Go."
    exit 1
fi

# Set Go Path (modify this if your Go path is different)
export GOPATH=$HOME/go

# Navigate to your Go project's directory (adjust as needed)
cd "$(dirname "$0")/.."

# Build the Go application
echo "Building $APP_NAME..."
go build -o $APP_NAME

# Check if build was successful
if [ $? -eq 0 ]; then
    echo "Build successful."
    # echo "Running $APP_NAME..."
    # ./$APP_NAME
else
    echo "Build failed."
    exit 1
fi