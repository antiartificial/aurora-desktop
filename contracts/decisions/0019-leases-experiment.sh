set -e
mount --make-rprivate / 2>/dev/null || true
mount --bind target.txt mnt/f
exec 7< mnt/f
echo "plain umount:  $(umount mnt/f 2>&1 && echo ok || true)"
umount -l mnt/f
echo "lazy umount:   ok (detached)"
echo "path now:      '$(cat mnt/f 2>&1 || true)'"
echo "held fd still: '$(cat <&7)'"
# The decisive question: does the retained fd see data written AFTER the lease ended?
echo "secret-v2-written-after-revoke" > target.txt
exec 9< /proc/self/fd/7
echo "via held fd:   '$(cat <&9 2>&1 || true)'"
