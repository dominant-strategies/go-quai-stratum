# Environment Setup
For the simplest installation process, we recommend installing and running go-quai-stratum on the same computer that you're running go-quai. Running go-quai-stratum on a separate computer is for advanced users as it requires additional networking configuration.
## Install Dependencies
To run an instance of go-quai-stratum, you'll need to install a few dependencies. You can install dependencies with your favorite package manager (apt, brew, etc.).
If you've already installed go-quai, you already have all the necessary dependencies to run go-quai-stratum.
### Go v1.21.0+
#### Snap Install (snapd is not installed by default on all Linux Distros):
```bash
sudo apt install snapd
```

#### Install go
```bash
sudo snap install go --classic
```

#### MacOS Install: 
```bash
brew install go
```

If you're not on Ubuntu or MacOS, instructions on how to install go directly can be found on the golang installation page.

### Git & Make
Linux install:
```bash
sudo apt install git make
```
MacOS install:
```bash
brew install git make
```
## Install go-quai-stratum
Now that you've installed the base dependencies, we can go ahead and clone the go-quai-stratum repo. 

To clone the go-quai-stratum repo and navigate to it, run the following commands:

```bash
git clone https://github.com/dominant-strategies/go-quai-stratum
cd go-quai-stratum
```

This command installs the main branch to your local machine. Unless you intend to develop, you must checkout the latest release.

You can find the latest release on the go-quai-stratum releases page.

Then, check out the latest release with:
```bash
git checkout put-latest-release-here
```
For example (this not the latest release, check the releases page for the latest release number)
```bash
git checkout v01.2.3-rc.4
```
# Configuration
To run the Quai stratum proxy, you'll need to do some minor configuration. Start by copying the example configuration file to a local configuration file.

```bash
cp config/config.example.json config/config.json
```
Within the config.json file, you'll be able to configure networking settings and other relevant variables.

# Running the Proxy
## Build
Before running the proxy, we need to build the source. You can build via Makefile by running the following command:
```bash
make go-quai-stratum
```
## Run
Now that we've built the source, we can start our proxy We recommend using a process manager program like tmux or screen to run the proxy.

To run the proxy, you'll need to know the web-socket ports utilized by the shard you want to mine within the node. More information on how and why to select shards can be found in our FAQ.

Pass the web-socket ports for the region and zone you'd like to point your proxy at as flags in the run command like so (replace REGION-WS-PORT and ZONE-WS-PORT with the web-socket ports of your desired slice):
```bash
./build/bin/go-quai-stratum --region=REGION-WS-PORT --zone=ZONE-WS-PORT
```
Do not open the below Web Socket ports EXCEPT in the specific case where your miner is on a different network than your node/stratum (and even then, be sure to only open the port to the necessary machine). You may be putting your local network security at risk.

Running the proxy will only work for chains your node is validating state for. Global nodes validate state for all chains, whereas slice nodes only validate state for the chains you specify.

The proxy by default listens for miner connections on the 3333 port. You can change the port the proxy listens on by passing it in with the --stratum flag in the run command if you'd like. 

```bash
./build/bin/go-quai-stratum --region=REGION-WS-PORT --zone=ZONE-WS-PORT --stratum=LISTENING-PORT
```

Alternatively, you can simply specify the name of ther region and zone you wish to mine to:
```bash
./build/bin/go-quai-stratum --region=tinos --zone=tinos1 --stratum=LISTENING-PORT
```
Changing the proxy listening port is useful for running multiple proxies on a single full node. If you're only mining on a single shard, there is no need to change the listening port.

The proxy should begin streaming logs to the terminal that look similar to below.

```bash
WARN[0000] One ethash cache must always be in memory requested=0
2023/09/06 13:47:17 main.go:45: Loading config: 0x14000032970
2023/09/06 13:47:17 main.go:84: Running with 4 threads
2023/09/06 13:47:17 policy.go:80: Set policy stats reset every 1h0m0s
2023/09/06 13:47:17 policy.go:84: Set policy state refresh every 1m0s
2023/09/06 13:47:17 policy.go:100: Running with 8 policy workers
WARN[0000] One ethash cache must always be in memory requested=0
2023/09/06 13:47:17 proxy.go:104: Set block refresh every 120ms
2023/09/06 13:47:17 stratum.go:38: Stratum listening on 0.0.0.0:3333
2023/09/06 13:47:17 proxy.go:294: New block to mine on Zone at height [1 1 1]
2023/09/06 13:47:17 proxy.go:295: Sealhash: 0xc4a31a763af09272a5da7f237978f5e0ead1e409c1e19a034ff8e40e7d727561
2023/09/06 13:47:17 proxy.go:228: Starting proxy on 0.0.0.0:0
2023/09/06 13:47:17 stratum.go:280: Broadcasting new job to 0 stratum miners
```
To stop the proxy, use CTRL+C in your terminal.
After configuring and pointing your proxy at a shard, you're now ready to point a GPU miner at it and start mining.
