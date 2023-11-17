#!/bin/bash

ROBOCNI="robocni"
LOOPROBOCNI="looprobocni"

# Ensure Go is installed
if ! command -v go &> /dev/null
then
    echo "Go could not be found. Please install Go."
    exit 1
fi

# Set Go Path
export GOPATH=$HOME/go

# Navigate to the root of your Go project (adjust as needed)
cd "$(dirname "$0")/.."

# Create the bin directory if it doesn't exist
mkdir -p bin

# Build the robocni binary
echo "Building $ROBOCNI..."
go build -o bin/$ROBOCNI ./cmd/robocni/$ROBOCNI.go

# Check if build was successful
if [ $? -ne 0 ]; then
    echo "Build failed for $ROBOCNI."
    exit 1
fi

# Build the looprobocni binary
echo "Building $LOOPROBOCNI..."
go build -o bin/$LOOPROBOCNI ./cmd/looprobocni/$LOOPROBOCNI.go

# Check if build was successful
if [ $? -ne 0 ]; then
    echo "Build failed for $LOOPROBOCNI."
    exit 1
fi

echo "Build successful."