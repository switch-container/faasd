// this fs switcher is aimed at overlayfs
// may not used directly for all containers' snapshotter
#include <chrono>
#include <fcntl.h>
#include <iostream>
#include <sched.h>
#include <stdio.h>
#include <string.h>
#include <sys/mount.h>
#include <sys/stat.h>
#include <sys/syscall.h> /* Definition of SYS_* constants */
#include <unistd.h>

#include "CLI11.hpp"

std::string pathJoin(const std::string &p1, const std::string &p2) {
  const char sep = '/';
  // Add separator if it is not included in the first path:
  if (p1[p1.length() - 1] != sep) {
    return p1 + "/" + p2;
  } else {
    return p1 + p2;
  }
}

void fatal(const char *msg) {
  perror(msg);
  exit(1);
}

int pivot_root(const char *new_root, const char *put_old) {
  return syscall(SYS_pivot_root, new_root, put_old);
}

int mount_overlay(const std::string &new_root, const std::string &lower,
                  const std::string &upper, const std::string &work) {
  // allocate 4K buffer
  char opts[4096];
  int len = sprintf(opts, "index=off,lowerdir=%s,upperdir=%s,workdir=%s",
                    lower.c_str(), upper.c_str(), work.c_str());
  if (len > 4096) {
    printf("options too long\n");
    return -1;
  }
  return mount("overlay", new_root.c_str(), "overlay", MS_MGC_VAL, opts);
}

// for now we only join mnt namespace to test overlay
int main(int argc, char *argv[]) {
  auto start = std::chrono::steady_clock::now();
  CLI::App app{
      "fs-switcher: a helper utility for setup overlay rootfs for container."};
  const std::string TMP_DIR = "__new_root";
  std::string old_root;
  std::string lowerdirs, upperdir, workdir, new_root;
  pid_t pid;
  app.add_option("--old-root", old_root, "original rootfs path of container")
      ->required();
  app.add_option("--lower", lowerdirs,
                 "the lowerdir of newly created overlayfs")
      ->required();
  app.add_option("--upper", upperdir, "the upperdir of newly created overlayfs")
      ->required();
  app.add_option("--work", workdir, "the workdir of newly created overlayfs")
      ->required();
  app.add_option("--new,--new-root", new_root, "the new rootfs")->required();
  app.add_option("pid", pid, "the pid of old container")->required();
  CLI11_PARSE(app, argc, argv);

  std::cout << "old " << old_root << ", new " << new_root << ", pid " << pid
            << std::endl;

  int original_mnt_ns_fd = open("/proc/self/ns/mnt", O_RDONLY);
  if (original_mnt_ns_fd < 0)
    fatal("open original mnt ns failed");

  char mnt_path[128];
  sprintf(mnt_path, "/proc/%d/ns/mnt", pid);
  printf("try to setns to %s\n", mnt_path);
  int mnt_ns_fd = open(mnt_path, O_RDONLY);
  if (mnt_ns_fd < 0)
    fatal("open target mnt ns failed");

  std::string tmp_root = pathJoin(old_root, TMP_DIR);
  printf("mount to tmp_root %s", tmp_root.c_str());
  if (mkdir(tmp_root.c_str(), 0755))
    fatal("mkdir for new root failed");
  if (mount_overlay(tmp_root, lowerdirs, upperdir, workdir))
    fatal("mount overlay failed");
  // auto end = std::chrono::steady_clock::now();
  // auto delta_us =
  //     std::chrono::duration_cast<std::chrono::microseconds>(end - start);
  // auto delta_ms =
  //     std::chrono::duration_cast<std::chrono::milliseconds>(end - start);
  // std::cout << "mount overlay time " << delta_us.count()
  //           << " us = " << delta_ms.count() << " ms" << std::endl;

  // start = std::chrono::steady_clock::now();
  if (setns(mnt_ns_fd, CLONE_NEWNS))
    fatal("set ns failed");
  char _tmp[128];
  getcwd(_tmp, sizeof(_tmp));
  printf("after setns cwd %s\n", _tmp);
  // end = std::chrono::steady_clock::now();
  // delta_us = std::chrono::duration_cast<std::chrono::microseconds>(end -
  // start); delta_ms =
  // std::chrono::duration_cast<std::chrono::milliseconds>(end - start);
  // std::cout << "setns to container time " << delta_us.count()
  //           << " us = " << delta_ms.count() << " ms" << std::endl;

  if (chdir(TMP_DIR.c_str()))
    fatal("chdir to new_root failed");
  // start = std::chrono::steady_clock::now();
  if (pivot_root(".", "."))
    fatal("pivot_root failed");
  // end = std::chrono::steady_clock::now();
  // delta_us = std::chrono::duration_cast<std::chrono::microseconds>(end -
  // start); delta_ms =
  // std::chrono::duration_cast<std::chrono::milliseconds>(end - start);
  // std::cout << "pivot_root time " << delta_us.count()
  //           << " us = " << delta_ms.count() << " ms" << std::endl;
  umount2(".", MNT_DETACH);

  // start = std::chrono::steady_clock::now();
  if (setns(original_mnt_ns_fd, CLONE_NEWNS))
    fatal("set ns failed");
  // end = std::chrono::steady_clock::now();
  // delta_us = std::chrono::duration_cast<std::chrono::microseconds>(end -
  // start); delta_ms =
  // std::chrono::duration_cast<std::chrono::milliseconds>(end - start);
  // std::cout << "setns to original time " << delta_us.count()
  //           << " us = " << delta_ms.count() << " ms" << std::endl;

  // start = std::chrono::steady_clock::now();
  if (mount(NULL, old_root.c_str(), NULL, MS_PRIVATE, NULL))
    fatal("make original overlay private failed");
  if (mount(tmp_root.c_str(), new_root.c_str(), NULL, MS_MOVE, NULL))
    fatal("move mount failed");
  umount2(old_root.c_str(), MNT_DETACH);
  auto end = std::chrono::steady_clock::now();
  auto delta_us =
      std::chrono::duration_cast<std::chrono::microseconds>(end - start);
  auto delta_ms =
      std::chrono::duration_cast<std::chrono::milliseconds>(end - start);
  std::cout << "total time " << delta_us.count() << " us = " << delta_ms.count()
            << " ms" << std::endl;
  return 0;
}
