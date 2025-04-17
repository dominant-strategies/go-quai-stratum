#!/usr/bin/env bash


stats_raw=`echo '{"id":0,"jsonrpc":"2.0","method":"miner_getstat1"}' | nc -w 3 localhost 21373 | jq '.result'`
# stats_raw=`cat ./sample_stats.json | jq '.result'`

if [[ $? -ne 0  || -z $stats_raw ]]; then
	echo -e "${YELLOW}Failed to read $miner stats_raw from localhost:21373${NOCOLOR}"
else
	khs=`echo $stats_raw | jq -r '.[2]' | awk -F';' '{print $1}'`
	#`echo $stats_raw | jq -r '.[3]' | awk 'gsub(";", "\n")' | jq -cs .` #send only hashes
	local tempfans=`echo $stats_raw | jq -r '.[6]' | tr ';' ' '` #"56 26  48 42"
	local temp=()
	local fan=()
	local tfcounter=0
	for tf in $tempfans; do
		(( $tfcounter % 2 == 0 )) &&
			temp+=($tf) ||
			fan+=($tf)
		((tfcounter++))
	done
	temp=`printf '%s\n' "${temp[@]}" | jq --raw-input . | jq --slurp -c .`
	fan=`printf '%s\n' "${fan[@]}" | jq --raw-input . | jq --slurp -c .`

	#ethminer API can show hashes, but no load... hard to fix it here
	#local hs=(`echo "$stats_raw" | jq -r '.[3]' | tr ';' ' '`)
	#echo ${hs[0]}

	local hs=`echo "$stats_raw" | jq -r '.[3]' | tr ';' '\n' | jq -cs '.'`

	local ac=`echo $stats_raw | jq -r '.[2]' | awk -F';' '{print $2}'`
	local rj=`echo $stats_raw | jq -r '.[2]' | awk -F';' '{print $3}'`
	local ver=`echo $stats_raw | jq -r '.[0]'`
	ver=`echo $ver | sed 's/^[[:alpha:]]\+//' | sed 's/^-//'`

	local algo="progpow"

	local stats_detail=`echo '{"id":0,"jsonrpc":"2.0","method":"miner_getstatdetail"}' | nc -w 3 localhost 21373 | jq '.result'`
	# local stats_detail=`cat detailled_stats.json | jq '.result'`
	if [[ $? -ne 0 || -z $stats_detail || $stats_detail == "null" ]]; then
		local bus_numbers=[]
		local iv=0
	else
		if [[ ! -z `echo $stats_detail | jq '. | to_entries[] | select(.key == "devices")'` ]]; then #version >= 0.18.0
			local bus_numbers=`echo $stats_detail | jq -r '.devices[].hardware.pci [0:2]' | awk -Wposix '{printf("%d\n","0x" $1)}' | jq -cs '.'`
			local iv=`echo $stats_detail | jq -r '.mining.shares[2]'`
			local iv_bus=`echo $stats_detail | jq '.devices[].mining.shares[2]' | jq -cs '.' | sed  's/,/;/g' | tr -d [ | tr -d ]`
		elif [[ ! -z `echo $stats_detail | jq '. | to_entries[] | select(.key == "gpus")'` ]]; then #version < 0.18.0
			local bus_numbers=[]
			local iv=`echo $stats_detail | jq '.shares.invalid'`
		else #version unknown
			local bus_numbers=[]
			local iv=0
		fi
	fi
	if [[ -z $bus_numbers || $bus_numbers == "[]" ]]; then
		stats=$(jq -n \
			--arg uptime "`echo \"$stats_raw\" | jq -r '.[1]' | awk '{print $1*60}'`" \
			--argjson hs "$hs" --argjson temp "$temp" --argjson fan "$fan" \
			--arg algo "$algo" \
			--arg ac "$ac" --arg rj "$rj" --arg iv "$iv" \
			--arg ver "$ver" \
			'{$hs, $temp, $fan, $uptime, $algo, ar: [$ac, $rj, $iv], $ver}')
	else
		stats=$(jq -n \
			--arg uptime "`echo \"$stats_raw\" | jq -r '.[1]' | awk '{print $1*60}'`" \
			--argjson hs "$hs" --argjson temp "$temp" --argjson fan "$fan" \
			--arg algo "$algo" \
			--arg ac "$ac" --arg rj "$rj" --arg iv "$iv" --arg iv_bus "$iv_bus" \
			--arg ver "$ver" \
			--argjson bus_numbers "$bus_numbers" \
			'{$hs, $temp, $fan, $uptime, $algo, ar: [$ac, $rj, $iv, $iv_bus], $bus_numbers, $ver}')
	fi
fi
