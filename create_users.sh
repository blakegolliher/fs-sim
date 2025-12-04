#!/bin/bash
# Create test users for fs-simulator
# These users are named after computer science pioneers
# Run this script as root before running the simulator

set -e

# Check if running as root
if [[ $EUID -ne 0 ]]; then
   echo "This script must be run as root"
   exit 1
fi

# Create a common group for all test users
GROUP_NAME="fssim"
GROUP_GID=2000

echo "Creating group $GROUP_NAME with GID $GROUP_GID..."
if getent group "$GROUP_NAME" > /dev/null 2>&1; then
    echo "Group $GROUP_NAME already exists, skipping..."
else
    groupadd -g "$GROUP_GID" "$GROUP_NAME"
    echo "Group $GROUP_NAME created."
fi

# Array of users: username:uid:full_name
declare -a USERS=(
    "ada:1001:Ada Lovelace"
    "turing:1002:Alan Turing"
    "hopper:1003:Grace Hopper"
    "dijkstra:1004:Edsger Dijkstra"
    "knuth:1005:Donald Knuth"
    "ritchie:1006:Dennis Ritchie"
    "thompson:1007:Ken Thompson"
    "torvalds:1008:Linus Torvalds"
    "gosling:1009:James Gosling"
    "wozniak:1010:Steve Wozniak"
    "cerf:1011:Vint Cerf"
    "bernerslee:1012:Tim Berners-Lee"
)

echo ""
echo "Creating ${#USERS[@]} test users..."
echo ""

for user_entry in "${USERS[@]}"; do
    IFS=':' read -r username uid fullname <<< "$user_entry"

    # Check if user already exists
    if id "$username" > /dev/null 2>&1; then
        existing_uid=$(id -u "$username")
        if [[ "$existing_uid" -eq "$uid" ]]; then
            echo "[OK] User $username already exists with correct UID $uid"
        else
            echo "[WARN] User $username exists but with UID $existing_uid (expected $uid)"
        fi
        continue
    fi

    # Create user with specific UID, primary group fssim, and also their own group
    echo "Creating user: $username (UID: $uid) - $fullname"

    # Create user's personal group with matching GID
    if ! getent group "$username" > /dev/null 2>&1; then
        groupadd -g "$uid" "$username"
    fi

    # Create the user
    useradd \
        --uid "$uid" \
        --gid "$uid" \
        --groups "$GROUP_NAME" \
        --comment "$fullname - FS Simulator Test User" \
        --create-home \
        --shell /bin/bash \
        "$username"

    echo "[CREATED] $username (UID: $uid, GID: $uid)"
done

echo ""
echo "User creation complete!"
echo ""
echo "Summary of created users:"
echo "========================="
printf "%-12s %-6s %-6s %s\n" "USERNAME" "UID" "GID" "FULL NAME"
echo "---------------------------------------------------------"
for user_entry in "${USERS[@]}"; do
    IFS=':' read -r username uid fullname <<< "$user_entry"
    if id "$username" > /dev/null 2>&1; then
        actual_uid=$(id -u "$username")
        actual_gid=$(id -g "$username")
        printf "%-12s %-6s %-6s %s\n" "$username" "$actual_uid" "$actual_gid" "$fullname"
    fi
done
echo ""
echo "All users are members of the '$GROUP_NAME' group."
