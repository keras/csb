# Promote root mount to shared propagation so Podman can propagate mounts
# across its own mount namespaces (Docker defaults to private propagation).
mount --make-rshared / 2>/dev/null || true

# cgroup v2 delegation (docker:dind recipe). Even a privileged container starts
# with the controllers listed in cgroup.controllers but NOT enabled in the
# root's cgroup.subtree_control, so podman can't enable them for the cgroups it
# creates (crun fails: "controller pids is not available"). cgroup v2 forbids
# enabling subtree_control on a cgroup that has processes directly in it, so
# first move our own processes into a child ("init") cgroup, then push every
# available controller into the root's subtree_control for children to inherit.
if [ -e /sys/fs/cgroup/cgroup.controllers ] && [ -w /sys/fs/cgroup/cgroup.subtree_control ]; then
    mkdir -p /sys/fs/cgroup/init
    while read -r _pid; do
        echo "$_pid" > /sys/fs/cgroup/init/cgroup.procs 2>/dev/null || true
    done < /sys/fs/cgroup/cgroup.procs
    for _ctrl in $(cat /sys/fs/cgroup/cgroup.controllers); do
        echo "+$_ctrl" > /sys/fs/cgroup/cgroup.subtree_control 2>/dev/null || true
    done
fi

# Make /proc/sys writable so netavark can configure network namespaces.
mount -o remount,rw /proc/sys 2>/dev/null || true
