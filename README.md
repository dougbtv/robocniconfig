# Robo CNI configuration

![](docs/robocni.png | width=350)

Automatically creates CNI configurations and net-attach-defs using [ollama](https://github.com/jmorganca/ollama).

# Usage

Clone this and build it with the `./hack/build-go.sh`.

```
export OLLAMA_HOST=192.168.50.199
./robocni "give me a macvlan CNI configuration mastered to eth0 using whereabouts ipam ranged on 192.0.2.0/24"
```

Or with output:

```
$ robocni --json "macvlan with whereabouts cni on 192.0.2.0/24"
{
    "cniVersion": "0.3.1",
    "name": "whereaboutsexample",
    "type": "macvlan",
    "master": "eth0",
    "mode": "bridge",
    "ipam": {
        "type": "whereabouts",
        "range": "192.0.2.0/24",
        "exclude": []
    }
}
```

# The "looprobocni" tool

This runs robocni in a loop and automatically creates the net-attach-defs using `kubectl` and then attaches pods to that network, makes a ping over them, and records the results.

Make sure `robocni` is in your path.

Put the hints in a `prompts.txt` file or pass the `--promptfile` parameter.

```
./looprobocni --runs 5000
```

Which would produce something like:

```
------------------ RUN # 12
User hint:  macvlan whereabouts 192.0.2.0/26
---
apiVersion: "k8s.cni.cncf.io/v1"
kind: NetworkAttachmentDefinition
metadata:
  name: whereaboutsexample
spec:
  config: '{
    "cniVersion": "0.3.1",
    "name": "whereaboutsexample",
    "type": "macvlan",
    "master": "eth0",
    "mode": "bridge",
    "ipam": {
        "type": "whereabouts",
        "range": "192.0.2.0/26"
    }
}'
Parsed name: whereaboutsexample
Spinning up pods...
Pod left is ready
Pod right is ready
IP Address for net1: 192.0.2.2
---
Run number: 12
Total Errors: 0 (0.00%)
Generation Errors: 0 (0.00%)
Failed Pod Creations: 0 (0.00%)
Ping Errors: 0 (0.00%)
Stats Array:
  Hint 1: Runs: 2, Successes: 2
  Hint 2: Runs: 1, Successes: 1
  Hint 3: Runs: 1, Successes: 1
  Hint 4: Runs: 3, Successes: 3
  Hint 5: Runs: 5, Successes: 5
  Hint 6: Runs: 0, Successes: 0
```