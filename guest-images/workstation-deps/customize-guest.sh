#!/usr/bin/env bash
set -euo pipefail

export DEBIAN_FRONTEND=noninteractive

cat >/tmp/osw-local.list <<'EOF'
deb [trusted=yes] file:/tmp/debs ./
EOF

apt_options=(
    -o Dir::Etc::sourcelist=/tmp/osw-local.list
    -o Dir::Etc::sourceparts=-
    -o APT::Get::List-Cleanup=0
)
apt-get "${apt_options[@]}" update
mapfile -t apt_packages </tmp/osw-apt-packages.txt
missing_packages=()
for package in "${apt_packages[@]}"; do
    if [[ "$(dpkg-query -W -f='${db:Status-Abbrev}' "${package}" 2>/dev/null || true)" != "ii " ]]; then
        missing_packages+=("${package}")
    fi
done
if ((${#missing_packages[@]})); then
    apt-get "${apt_options[@]}" install \
        -y \
        --no-install-recommends \
        --no-remove \
        --no-upgrade \
        "${missing_packages[@]}"
fi

for command_name in \
    evince gimp google-chrome libreoffice nautilus thunderbird vlc
do
    command -v "${command_name}" >/dev/null
done

node_version=20.19.5
tar -xJf "/tmp/node-v${node_version}-linux-x64.tar.xz" -C /opt
ln -sfn "/opt/node-v${node_version}-linux-x64" /opt/node20
rm -rf /opt/osw-node-runtime
mv /tmp/node-runtime /opt/osw-node-runtime
ln -sfn /opt/node20/bin/node /usr/local/bin/node
ln -sfn /opt/node20/bin/npm /usr/local/bin/npm
ln -sfn /opt/node20/bin/npx /usr/local/bin/npx
ln -sfn /opt/node20/bin/corepack /usr/local/bin/corepack
ln -sfn /opt/osw-node-runtime/node_modules/.bin/playwright \
    /usr/local/bin/playwright
export PATH="/opt/node20/bin:/opt/osw-node-runtime/node_modules/.bin:${PATH}"

rm -rf /opt/osw-workstation
python3 -m venv /opt/osw-workstation
/opt/osw-workstation/bin/python -m pip install \
    --disable-pip-version-check \
    --no-cache-dir \
    --no-index \
    --find-links /tmp/wheelhouse \
    --upgrade \
    pip==25.2 \
    setuptools==80.9.0 \
    wheel==0.45.1
/opt/osw-workstation/bin/python -m pip install \
    --disable-pip-version-check \
    --no-cache-dir \
    --no-index \
    --find-links /tmp/wheelhouse \
    --requirement /tmp/osw-python-packages.txt
/opt/osw-workstation/bin/python -m pip check

cat >/etc/profile.d/osw-workstation-deps.sh <<'EOF'
export PATH="/opt/node20/bin:/opt/osw-node-runtime/node_modules/.bin:${PATH}:/opt/osw-workstation/bin"
export NODE_PATH="/opt/osw-node-runtime/node_modules${NODE_PATH:+:${NODE_PATH}}"
export PYTHONPATH="/opt/osw-workstation/lib/python3.10/site-packages${PYTHONPATH:+:${PYTHONPATH}}"
EOF
chmod 0644 /etc/profile.d/osw-workstation-deps.sh

cat >/usr/local/bin/osw-workstation-python <<'EOF'
#!/usr/bin/env bash
exec /opt/osw-workstation/bin/python "$@"
EOF
chmod 0755 /usr/local/bin/osw-workstation-python

install -d -m 0755 /usr/local/share/osw-workstation-deps
cp /tmp/osw-apt-packages.txt \
    /usr/local/share/osw-workstation-deps/apt-packages.txt
cp /tmp/osw-python-packages.txt \
    /usr/local/share/osw-workstation-deps/python-packages.txt
cp /tmp/osw-node-packages.txt \
    /usr/local/share/osw-workstation-deps/node-packages.txt
dpkg-query -W -f='${Package}\t${Version}\n' \
    > /usr/local/share/osw-workstation-deps/dpkg-manifest.tsv
/opt/osw-workstation/bin/python -m pip freeze \
    > /usr/local/share/osw-workstation-deps/pip-freeze.txt
/opt/node20/bin/npm --prefix /opt/osw-node-runtime list --depth=0 --json \
    > /usr/local/share/osw-workstation-deps/npm-manifest.json
printf '%s\n' "${node_version}" \
    > /usr/local/share/osw-workstation-deps/node-version.txt

apt-get clean
rm -rf \
    /var/lib/apt/lists/* \
    /root/.cache/pip \
    /tmp/node-v*-linux-x64.tar.xz \
    /tmp/debs \
    /tmp/wheelhouse
