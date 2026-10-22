# Desktop image setup

The default installation adds ordinary desktop applications to a fresh Ubuntu
OSWorld guest. It preserves the existing LibreOffice, browser and benchmark
software versions. Package dependencies can require upgrades; review the apt
plan before installation. The scripts do not launch a benchmark or install HCU.

Use a disposable copy of the base image. Run the scripts inside that guest,
then remove the staging directory before exporting the image. Keep these
operator scripts and software archives outside the evaluated agent's workspace.

## Prepare software archives

`manifest.json` selects the application archives and desktop configuration.
`freecad-packages.json` selects Ubuntu's FreeCAD and OCCT packages. These files
record versions, filenames and package metadata. They do not attest archive
contents. Use software archives from the corresponding upstream distributions.

1. Create an asset directory readable by the guest desktop account.
2. Download Joplin and Slicer from the URLs in `manifest.json`.
3. Place the GeoGebra portable archive and KiCad offline bundle in that directory.
4. In Ubuntu with matching apt repositories, build the private FreeCAD archive:

```bash
python3 prepare_freecad.py --output /tmp/desktop-assets/freecad-private.tar
```

The filenames must match `manifest.json`. Copy only software packages from an
OSWorld asset distribution. Do not copy task documents, evaluator materials or
credentials. No binary archives belong in this repository.

## Install in a fresh guest

The examples assume that the desktop account is `user`. Replace that account
with the guest's actual account.

1. Copy this directory and the software archives to a temporary guest directory.
2. Update apt's package index and inspect its proposed dependency changes.
3. Check the guest platform and archive filenames:

```bash
python3 install.py --user user --assets /tmp/desktop-assets --check
```

4. Install the applications and preferences as root:

```bash
sudo python3 install.py --user user --assets /tmp/desktop-assets
```

Use repeated `--phase` options to select `gtk`, `qt`, `joplin`, `special`,
`preferences` or `support`. Installation stops when a command fails. Joplin
refuses to replace an existing installation. Use a fresh base copy when changing
the selected software rather than rerunning over an unknown partial image.

5. Start a fresh desktop session and verify application startup and input.
6. Remove the copied scripts, archives and extracted staging files.
7. Shut down the guest and export a new image without replacing the base.

`--check` validates the operating system, architecture and required filenames.
It does not run apt, verify archive contents or prove application behavior.

## Optional LibreOffice installation

`install_libreoffice.py` installs the official release selected in
`libreoffice.json` beside the distribution version. This separate command does
not run as part of `install.py`. It validates the exact package set, versions
and architecture before installing. It creates an isolated profile and sets
ordinary preferences to suppress welcome, tip and template dialogs.

Download the archive from the manifest's source, then run the installer as root
with `--archive /path/to/the/archive.tar.gz --desktop-user user`. The archive's
basename must match the manifest. The default preserves the existing commands
and user profile. Add `--switch-default` only when selecting this release for
the image. That option adds `/usr/local/bin/libreoffice` and `soffice` symlinks;
the distribution commands remain in `/usr/bin`. Close candidate applications
before installation, then verify Writer, Calc and Impress in a fresh session.

## Repairs and configuration

| Component | Installed behavior |
| --- | --- |
| FreeCAD | Uses private OCCT libraries so KiCad can install its incompatible system OCCT packages. System Qt preserves accessibility. |
| Spyder | Passes integer image dimensions to Qt, preventing startup failures with stricter Python bindings. Unknown source patterns fail. |
| GeoGebra | Restores executable permissions on its launcher, Java executable and Java process helper. |
| VS Code | Enables editor accessibility for the next application launch while preserving other JSON settings. |
| Joplin | Installs the upstream AppImage contents with the Chromium sandbox enabled. |
| REAPER | Selects the guest's PulseAudio backend. |
| WPS, MuseScore and Slicer | Save desktop preferences that suppress first-launch prompts and update checks. |
| OpenBoard | Installs the selected Snap channel. The channel can advance independently of this manifest. |
| Support | Installs NSS certificate tools and IBus bindings. Certificate import and the active desktop input source remain the deployment's responsibility. |

FreeCAD's private package prefix avoids global library replacement. Its launcher
sets library paths only for FreeCAD. The scripts neither force conflicting
packages into dpkg nor copy benchmark solutions into the guest.

The package sources remain Ubuntu repositories and upstream release archives.
Apt dependency versions and Snap revisions are not frozen by this manifest.
Record the installed package list and Snap revision alongside the exported image.
Restart applications after setting preferences. GUI and accessibility checks
must run against the exported image before the operator selects it for evaluation.

Run the focused script tests without installing packages:

```bash
python3 -m unittest discover -s scripts/images/osworld -p 'test_*.py'
```
