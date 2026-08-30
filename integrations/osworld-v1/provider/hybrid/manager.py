"""osw-hybrid VMManager —— 极简实现。

官方各 provider 的 manager 承担「下载/注册/回收 VM 镜像文件」的职责。
osw-hybrid 走容器原生桌面，镜像由 docker 自己管理，没有 qcow2 要下载、没有注册表要维护，
因此除了 get_vm_path 之外全是 no-op。

get_vm_path 返回镜像名（而非文件路径），provider.start_emulator 会拿它当镜像用，
这样切换 v2/v3 做对比实验只需要改一个环境变量。
"""

import logging
import os

from desktop_env.providers.base import VMManager

logger = logging.getLogger("desktopenv.providers.hybrid.HybridVMManager")
logger.setLevel(logging.INFO)

DEFAULT_IMAGE = os.environ.get("OSW_HYBRID_IMAGE", "osw-hybrid:v0.14")


class HybridVMManager(VMManager):
    def __init__(self, registry_path=""):
        pass

    def add_vm(self, vm_path, **kwargs):
        pass

    def check_and_clean(self, **kwargs):
        pass

    def delete_vm(self, vm_path, region=None, **kwargs):
        pass

    def initialize_registry(self, **kwargs):
        pass

    def list_free_vms(self, **kwargs):
        return [DEFAULT_IMAGE]

    def occupy_vm(self, vm_path, pid, region=None, **kwargs):
        pass

    def get_vm_path(self, os_type="Ubuntu", region=None, screen_size=(1920, 1080), **kwargs):
        if os_type != "Ubuntu":
            raise NotImplementedError(
                f"osw-hybrid provider only supports Ubuntu containers, got os_type={os_type!r}"
            )
        return DEFAULT_IMAGE
