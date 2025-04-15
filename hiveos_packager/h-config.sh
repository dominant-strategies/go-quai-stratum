#!/usr/bin/env bash

mkfile_from_symlink $CUSTOM_CONFIG_FILENAME

local user_config=`echo $CUSTOM_USER_CONFIG`

#generating config
echo "$user_config" > $CUSTOM_CONFIG_FILENAME

