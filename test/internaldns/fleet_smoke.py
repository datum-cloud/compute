#!/usr/bin/env python3
"""Run the production Compute publisher through the disposable DNS serving fleet."""
import argparse
import hashlib
import importlib.util
import json
from pathlib import Path
import sys

ROOT = Path(__file__).resolve().parents[2]


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--dns-repo", type=Path, default=ROOT.parent / "dns-operator")
    parser.add_argument("--publisher-binary", type=Path, help="Use a prebuilt test runner")
    parser.add_argument("--results", type=Path, default=ROOT / "test/internaldns/results/fleet-smoke.json")
    args = parser.parse_args()
    dns = args.dns_repo.resolve()
    spec = importlib.util.spec_from_file_location("dns_fleet", dns / "test/internaldns/run.py")
    fleet = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(fleet)
    h = fleet.Harness(False, args.results)
    error = None

    def obj(kind, name):
        return json.loads(h.kubectl("-n", fleet.PROJECT_NS, "get", kind, name, "-o", "json").stdout)

    def status(name, allocated=True, address="10.0.1.30", app_ready=False):
        condition = lambda typ, value: {"type": typ, "status": str(value), "reason": "TestObservation", "message": "Smoke fixture", "lastTransitionTime": "2026-10-07T00:00:00Z"}
        value = {"status": {
            "conditions": [condition("Ready", "True" if app_ready else "False")],
            "networkInterfaces": [{"name": "eth0", "addresses": [
                {"family": "IPv4", "address": address + "/32"},
                {"family": "IPv6", "address": "2001:db8::30/128"},
            ], "conditions": [condition("Allocated", "True" if allocated else "False"), condition("Programmed", "True")] }],
        }}
        h.kubectl("-n", fleet.PROJECT_NS, "patch", "instance", name, "--subresource=status", "--type=merge", "-p", json.dumps(value))

    def query(server, name, rrtype="A", tcp=False):
        output = h.compose("exec", "-T", "probe", "dig", "@" + server, name, rrtype, "+noall", "+comments", "+answer", *(["+tcp"] if tcp else [])).stdout
        code = next((line.split("status:", 1)[1].split(",", 1)[0].strip() for line in output.splitlines() if "status:" in line), "UNKNOWN")
        answers = [line.split()[-1] for line in output.splitlines() if not line.startswith(";") and len(line.split()) >= 5 and line.split()[-2] == rrtype]
        return code, answers

    def expect(label, server, name, answers, rrtype="A", tcp=False, absent=False):
        observed = {}
        def matches():
            code, found = query(server, name, rrtype, tcp)
            observed["status"] = code
            return (code in ("NOERROR", "NXDOMAIN", "REFUSED", "SERVFAIL") if absent else code == "NOERROR") and found == answers
        h.wait(label, matches, timeout=75, interval=0.5)
        h.pass_check(label, name=name, recordType=rrtype, answers=answers, tcp=tcp, **observed)

    try:
        binary = args.publisher_binary.resolve() if args.publisher_binary else h.work / "compute-dns-publisher"
        if not args.publisher_binary:
            h.run("go", "build", "-o", str(binary), "./test/internaldns/publisher", cwd=ROOT)
        disabled = h.run(str(binary), "--kubeconfig", "/does/not/exist")
        if "disabled" not in disabled.stdout:
            raise AssertionError("default-off runner tried to start")
        h.pass_check("default-off publisher starts without a Kubernetes/DNS configuration")
        h.setup()
        h.kubectl("apply", "-f", str(ROOT / "config/base/crd/bases/compute.datumapis.com_instances.yaml"))
        role = obj("role", "compute-dns-publisher")
        role["rules"].extend([
            {"apiGroups": ["compute.datumapis.com"], "resources": ["instances"], "verbs": ["get", "list", "watch"]},
            {"apiGroups": ["networking.datumapis.com"], "resources": ["networks"], "verbs": ["get"]},
        ])
        h.kubectl("replace", "-f", "-", stdin=json.dumps(role))
        configs = h.write_configs()
        h.start("control", *h.command("control-plane", configs["control"]))
        h.install_webhook()
        h.kubectl("apply", "-f", str(dns / "test/internaldns/fixtures/vpc-a.yaml"))
        h.create_association("prod-a-vpc-a", "prod-a", "vpc-a")
        h.publish("prod-a", "bootstrap", "bootstrap", "10.0.1.1")
        bindings = h.sync_addresses(1)
        a_uid = obj("network", "vpc-a")["metadata"]["uid"]
        vip_a = bindings[a_uid]["spec"]["consumerAddress"]
        h.wait_for_bootstrap()
        for member in ("node-0", "cluster-0"):
            h.start_container(member, configs[member], network="datum-internal-dns_dns")
        for member in ("pdns-0", "pdns-1"):
            h.start_container(member, configs[member], network="container:datum-internal-dns-cluster-bind-1")
        h.wait_for_agent_leases(("node-0", "cluster-0", "pdns-0", "pdns-1"))
        for member in ("node-0", "cluster-0", "pdns-0", "pdns-1"):
            h.start_container(member + "-watchdog", configs[member + "-watchdog"], network="none", watchdog=True)
        h.kubectl("apply", "-f", str(dns / "test/internaldns/fixtures/vpc-b.yaml"))
        h.create_association("prod-b-vpc-b", "prod-b", "vpc-b")
        bindings = h.sync_addresses(2)
        b_uid = obj("network", "vpc-b")["metadata"]["uid"]
        vip_b = bindings[b_uid]["spec"]["consumerAddress"]
        process_ids = h.container_ids()
        h.start("compute", str(binary), "--feature-gates=InternalDNSPublishing=true", "--kubeconfig", str(h.compute_kubeconfig), "--lease=60s")
        instance = {"apiVersion": "compute.datumapis.com/v1alpha", "kind": "Instance", "metadata": {"name": "web-01", "namespace": fleet.PROJECT_NS}, "spec": {
            "runtime": {"resources": {"instanceType": "datumcloud/d1-standard-2"}, "sandbox": {"containers": [{"name": "web", "image": "docker.io/library/nginx:stable"}]}},
            "networkInterfaces": [{"name": "eth0", "network": {"name": "vpc-a"}}],
        }}
        h.kubectl("apply", "-f", "-", stdin=json.dumps(instance))
        status("web-01")

        def publication(instance_name):
            uid = obj("instance", instance_name)["metadata"]["uid"]
            registrations = json.loads(h.kubectl("-n", fleet.PROJECT_NS, "get", "dnsregistrations", "-l", "internal-dns.compute.datumapis.com/instance-uid=" + uid, "-o", "json").stdout)["items"]
            if not registrations:
                return None
            reg = registrations[0]
            zone = obj("dnszone", reg["spec"]["dnsZoneRef"]["name"])
            return reg, reg["spec"]["name"] + "." + zone["spec"]["domainName"].rstrip(".")
        reg, fqdn = h.wait("production Compute registration", lambda: publication("web-01"))
        instance["metadata"]["name"] = "web-02"
        instance["spec"]["networkInterfaces"][0]["network"]["name"] = "vpc-b"
        h.kubectl("apply", "-f", "-", stdin=json.dumps(instance))
        status("web-02", address="10.0.1.20")
        _, fqdn_b = h.wait("second VPC Compute registration", lambda: publication("web-02"))
        for tcp in (False, True):
            expect("second VPC serves its own Compute IPv4" + (" TCP" if tcp else " UDP"), vip_b, fqdn_b, ["10.0.1.20"], tcp=tcp)
            expect("second VPC serves its own Compute IPv6" + (" TCP" if tcp else " UDP"), vip_b, fqdn_b, ["2001:db8::30"], rrtype="AAAA", tcp=tcp)
        for tcp in (False, True):
            expect("production Compute IPv4" + (" TCP" if tcp else " UDP"), vip_a, fqdn, ["10.0.1.30"], tcp=tcp)
            expect("production Compute IPv6" + (" TCP" if tcp else " UDP"), vip_a, fqdn, ["2001:db8::30"], rrtype="AAAA", tcp=tcp)
            expect("other VPC cannot resolve instance" + (" TCP" if tcp else " UDP"), vip_b, fqdn, [], tcp=tcp, absent=True)
        h.pass_check("application Ready=False leaves instance identity published")
        status("web-01", address="10.0.1.31", app_ready=True)
        expect("address update reaches DNS", vip_a, fqdn, ["10.0.1.31"])
        status("web-01", allocated=False)
        expect("interface deallocation withdraws IPv4", vip_a, fqdn, [], absent=True)
        expect("interface deallocation withdraws IPv6", vip_a, fqdn, [], rrtype="AAAA", absent=True)
        expect("other VPC retains its Compute record during withdrawal", vip_b, fqdn_b, ["10.0.1.20"])
        status("web-01")
        expect("interface recovery republishes", vip_a, fqdn, ["10.0.1.30"])
        h.stop("compute")
        expect("stopped publisher lease expires", vip_a, fqdn, [], absent=True)
        h.start("compute", str(binary), "--feature-gates=InternalDNSPublishing=true", "--kubeconfig", str(h.compute_kubeconfig), "--lease=60s")
        expect("restarted publisher renews observation", vip_a, fqdn, ["10.0.1.30"])
        h.kubectl("-n", fleet.PROJECT_NS, "delete", "instance", "web-01", "--wait=true")
        expect("instance deletion withdraws answer", vip_a, fqdn, [], absent=True)
        expect("other VPC retains its Compute record during deletion", vip_b, fqdn_b, ["10.0.1.20"])
        h.kubectl("-n", fleet.PROJECT_NS, "delete", "instance", "web-02", "--wait=true")
        h.wait("instance DNS objects garbage collected", lambda: all(not json.loads(h.kubectl("-n", fleet.PROJECT_NS, "get", kind, "-l", "internal-dns.compute.datumapis.com/managed-by=compute-instance-publisher", "-o", "json").stdout)["items"] for kind in ("dnsregistrations", "dnscontributiongrants", "dnsrecordcontributions")))
        h.pass_check("instance deletion cleans registration, grant and contribution")
        if process_ids != h.container_ids():
            raise AssertionError("serving process count/identity changed")
        h.pass_check("shared serving process identities remain constant")
    except BaseException as exc:
        error = exc
        print("FAIL", exc, file=sys.stderr)
        for name in ("compute", "control"):
            log = h.logs / (name + ".log")
            if log.exists():
                print(name + ": " + log.read_text()[-6000:], file=sys.stderr)
    finally:
        h.write_results(error)
        data = json.loads(args.results.read_text())
        data["suite"] = "compute-internal-dns-fleet-smoke"
        data["computePublisherSourceSHA256"] = hashlib.sha256((ROOT / "internal/controller/internaldns_publisher.go").read_bytes()).hexdigest()
        if binary.is_file():
            data["computePublisherBinarySHA256"] = hashlib.sha256(binary.read_bytes()).hexdigest()
        data["limitations"] = ["Instance addressing/status are fixtures; no guest boots or real NSO allocation.", "One local source API and one local broker; no regional HA or Milo IAM deployment validation.", "Uses the production Compute publisher controller, started by a test runner; default-off production wiring is covered separately."]
        args.results.write_text(json.dumps(data, indent=2) + "\n")
        h.cleanup()
    if error:
        raise error
    print(f"PASS {len(fleet.RESULTS)} full-path checks")


if __name__ == "__main__":
    main()
