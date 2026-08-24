#!/bin/bash
# Create 100 test users (UID/GID 1000-1099) for fs-simulator
# Named after computer science pioneers and mathematics innovators
# Matches the uid/gid range in config-200m-mix.yaml
# Run this script as root before running the simulator

set -e

if [[ $EUID -ne 0 ]]; then
   echo "This script must be run as root"
   exit 1
fi

# Common group for all test users
GROUP_NAME="fssim"
GROUP_GID=2000

echo "Creating group $GROUP_NAME with GID $GROUP_GID..."
if getent group "$GROUP_NAME" > /dev/null 2>&1; then
    echo "Group $GROUP_NAME already exists, skipping..."
else
    groupadd -g "$GROUP_GID" "$GROUP_NAME"
    echo "Group $GROUP_NAME created."
fi

# 100 pioneers: username:uid:full_name
declare -a USERS=(
    # Foundations of computing
    "ada:1000:Ada Lovelace"
    "babbage:1001:Charles Babbage"
    "turing:1002:Alan Turing"
    "hopper:1003:Grace Hopper"
    "vonneumann:1004:John von Neumann"
    "shannon:1005:Claude Shannon"
    "church:1006:Alonzo Church"
    "boole:1007:George Boole"
    "curry:1008:Haskell Curry"
    "kleene:1009:Stephen Kleene"
    # Programming language pioneers
    "mccarthy:1010:John McCarthy"
    "backus:1011:John Backus"
    "kay:1012:Alan Kay"
    "liskov:1013:Barbara Liskov"
    "knuth:1014:Donald Knuth"
    "dijkstra:1015:Edsger Dijkstra"
    "hoare:1016:Tony Hoare"
    "wirth:1017:Niklaus Wirth"
    "perlis:1018:Alan Perlis"
    "iverson:1019:Kenneth Iverson"
    # Systems and OS
    "ritchie:1020:Dennis Ritchie"
    "thompson:1021:Ken Thompson"
    "kernighan:1022:Brian Kernighan"
    "pike:1023:Rob Pike"
    "torvalds:1024:Linus Torvalds"
    "stallman:1025:Richard Stallman"
    "joy:1026:Bill Joy"
    "hamilton:1027:Margaret Hamilton"
    "stroustrup:1028:Bjarne Stroustrup"
    "gosling:1029:James Gosling"
    # Networking and web
    "cerf:1030:Vint Cerf"
    "bernerslee:1031:Tim Berners-Lee"
    "diffie:1032:Whitfield Diffie"
    "hellman:1033:Martin Hellman"
    "rivest:1034:Ron Rivest"
    "shamir:1035:Adi Shamir"
    "lamport:1036:Leslie Lamport"
    "postel:1037:Jon Postel"
    "metcalfe:1038:Robert Metcalfe"
    "kahn:1039:Bob Kahn"
    # Algorithms and complexity
    "tarjan:1040:Robert Tarjan"
    "karp:1041:Richard Karp"
    "cook:1042:Stephen Cook"
    "rabin:1043:Michael Rabin"
    "bellman:1044:Richard Bellman"
    "floyd:1045:Robert Floyd"
    "huffman:1046:David Huffman"
    "hamming:1047:Richard Hamming"
    "sedgewick:1048:Robert Sedgewick"
    "levin:1049:Leonid Levin"
    # Databases and AI
    "codd:1050:Edgar Codd"
    "minsky:1051:Marvin Minsky"
    "simon:1052:Herbert Simon"
    "pearl:1053:Judea Pearl"
    "hinton:1054:Geoffrey Hinton"
    "chomsky:1055:Noam Chomsky"
    "milner:1056:Robin Milner"
    "aho:1057:Alfred Aho"
    "ullman:1058:Jeffrey Ullman"
    "cormen:1059:Thomas Cormen"
    # Classical mathematicians
    "euclid:1060:Euclid of Alexandria"
    "archimedes:1061:Archimedes of Syracuse"
    "euler:1062:Leonhard Euler"
    "gauss:1063:Carl Friedrich Gauss"
    "newton:1064:Isaac Newton"
    "leibniz:1065:Gottfried Leibniz"
    "fermat:1066:Pierre de Fermat"
    "pascal:1067:Blaise Pascal"
    "fibonacci:1068:Leonardo Fibonacci"
    "descartes:1069:Rene Descartes"
    # Analysis and algebra
    "fourier:1070:Joseph Fourier"
    "laplace:1071:Pierre-Simon Laplace"
    "lagrange:1072:Joseph-Louis Lagrange"
    "riemann:1073:Bernhard Riemann"
    "cauchy:1074:Augustin-Louis Cauchy"
    "hilbert:1075:David Hilbert"
    "noether:1076:Emmy Noether"
    "galois:1077:Evariste Galois"
    "abel:1078:Niels Henrik Abel"
    "jacobi:1079:Carl Jacobi"
    # Modern mathematics
    "cantor:1080:Georg Cantor"
    "godel:1081:Kurt Godel"
    "poincare:1082:Henri Poincare"
    "ramanujan:1083:Srinivasa Ramanujan"
    "erdos:1084:Paul Erdos"
    "kolmogorov:1085:Andrey Kolmogorov"
    "markov:1086:Andrey Markov"
    "bayes:1087:Thomas Bayes"
    "nash:1088:John Nash"
    "conway:1089:John Horton Conway"
    # Applied math and signal processing
    "mandelbrot:1090:Benoit Mandelbrot"
    "bernoulli:1091:Daniel Bernoulli"
    "poisson:1092:Simeon Denis Poisson"
    "dantzig:1093:George Dantzig"
    "karatsuba:1094:Anatolii Karatsuba"
    "strassen:1095:Volker Strassen"
    "cooley:1096:James Cooley"
    "tukey:1097:John Tukey"
    "viterbi:1098:Andrew Viterbi"
    "kruskal:1099:Joseph Kruskal"
)

CREATED=0
SKIPPED=0

echo ""
echo "Creating ${#USERS[@]} test users (CS pioneers & math innovators)..."
echo ""

for user_entry in "${USERS[@]}"; do
    IFS=':' read -r username uid fullname <<< "$user_entry"

    if id "$username" > /dev/null 2>&1; then
        existing_uid=$(id -u "$username")
        if [[ "$existing_uid" -eq "$uid" ]]; then
            echo "[OK] $username already exists with UID $uid"
        else
            echo "[WARN] $username exists but with UID $existing_uid (expected $uid)"
        fi
        ((SKIPPED++))
        continue
    fi

    # Create personal group with matching GID
    if ! getent group "$username" > /dev/null 2>&1; then
        if getent group "$uid" > /dev/null 2>&1; then
            echo "[WARN] GID $uid already taken, skipping group creation for $username"
        else
            groupadd -g "$uid" "$username"
        fi
    fi

    useradd \
        --uid "$uid" \
        --gid "$uid" \
        --groups "$GROUP_NAME" \
        --comment "$fullname - FS Simulator" \
        --no-create-home \
        --shell /sbin/nologin \
        "$username"

    echo "[CREATED] $username (UID: $uid) - $fullname"
    ((CREATED++))
done

echo ""
echo "Done: $CREATED created, $SKIPPED skipped (already existed)"
echo "All users are members of the '$GROUP_NAME' group."
