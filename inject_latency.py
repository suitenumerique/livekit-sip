import sys
import re
import os
import pydot
import statistics

def parse_log_file(log_file_path):
    """
    Parses the log file to extract latency data for each element.
    Returns a dictionary mapping element-id (hex string) to a dictionary:
    {
        'name': str,
        'times': list of int (nanoseconds),
        'min': int,
        'max': int,
        'avg': float,
        'median': float
    }
    """
    latency_data = {}
    # Regex to match: element-latency, element-id=(string)0x..., element=(string)NAME, ..., time=(guint64)1234
    pattern = re.compile(r'element-latency.*element-id=\(string\)(0x[0-9a-fA-F]+), element=\(string\)([^,]+).*time=\(guint64\)(\d+)')
    
    try:
        with open(log_file_path, 'r') as f:
            for line in f:
                match = pattern.search(line)
                if match:
                    element_id = match.group(1)
                    element_name = match.group(2)
                    time_ns = int(match.group(3))
                    
                    if element_id not in latency_data:
                        latency_data[element_id] = {
                            'name': element_name,
                            'times': []
                        }
                    latency_data[element_id]['times'].append(time_ns)
    except FileNotFoundError:
        print(f"Error: Log file '{log_file_path}' not found.")
        sys.exit(1)

    # Compute statistics once
    for element_id, data in latency_data.items():
        times = data['times']
        if times:
            data['min'] = min(times)
            data['max'] = max(times)
            data['avg'] = statistics.mean(times)
            data['median'] = statistics.median(times)
        else:
            # Should technically not happen if we only create entries on match, but safe default
            data['min'] = 0
            data['max'] = 0
            data['avg'] = 0.0
            data['median'] = 0.0

    return latency_data

def format_latency(ns):
    """Formats nanoseconds into a readable string."""
    if ns < 1000:
        return f"{ns}ns"
    elif ns < 1000000:
        return f"{ns/1000:.2f}us"
    elif ns < 1000000000:
        return f"{ns/1000000:.2f}ms"
    else:
        return f"{ns/1000000000:.2f}s"

def print_latency_table(latency_data):
    """
    Prints a sorted table of elements with their latency statistics.
    Sorted by Avg latency descending.
    """
    if not latency_data:
        print("No latency data found.")
        return

    # Prepare rows
    rows = []
    for element_id, data in latency_data.items():
        if not data['times']:
            continue
        
        count = len(data['times'])
        
        rows.append({
            'name': data['name'],
            'id': element_id,
            'count': count,
            'min': data['min'],
            'max': data['max'],
            'avg': data['avg'],
            'median': data['median']
        })

    # Sort by Median latency descending
    rows.sort(key=lambda x: x['median'], reverse=True)

    # Define columns and widths
    headers = ["Element Name", "Element ID", "Count", "Median", "Avg", "Min", "Max"]
    # Calculate widths
    w_name = max(len(r['name']) for r in rows) if rows else 12
    w_name = max(w_name, len(headers[0]))
    
    w_id = max(len(r['id']) for r in rows) if rows else 12
    w_id = max(w_id, len(headers[1]))
    
    w_count = 8
    w_lat = 12

    # Print Header
    header_fmt = f"{{:<{w_name}}}  {{:<{w_id}}}  {{:>{w_count}}}  {{:>{w_lat}}}  {{:>{w_lat}}}  {{:>{w_lat}}}  {{:>{w_lat}}}"
    print("-" * (w_name + w_id + w_count + 4 * w_lat + 12))
    print(header_fmt.format(*headers))
    print("-" * (w_name + w_id + w_count + 4 * w_lat + 12))

    # Print Rows
    for row in rows:
        print(header_fmt.format(
            row['name'],
            row['id'],
            row['count'],
            format_latency(row['median']),
            format_latency(row['avg']),
            format_latency(row['min']),
            format_latency(row['max'])
        ))
    print("-" * (w_name + w_id + w_count + 4 * w_lat + 12))

def process_dot_file(dot_file_path, latency_data):
    """
    Reads the DOT file, injects latency statistics into matching clusters,
    and writes the result to a new file.
    """
    try:
        graphs = pydot.graph_from_dot_file(dot_file_path)
    except Exception as e:
        print(f"Error parsing DOT file: {e}")
        sys.exit(1)

    if not graphs:
        print("Error: No graph found in the DOT file.")
        sys.exit(1)

    graph = graphs[0]
    
    def process_subgraph(subgraph):
        name = subgraph.get_name()
        
        # Regex to capture the hex address at the end of the cluster name
        match = re.search(r'(0x[0-9a-fA-F]+)$', name.strip('"'))
        
        if match:
            element_id = match.group(1)
            if element_id in latency_data:
                data = latency_data[element_id]
                if data['times']:
                    label_attr = subgraph.get_label()
                    if label_attr:
                        label_str = label_attr.strip('"')
                        
                        # We use literal \n for DOT label newline
                        stats_str = (
                            f"\\nLatency:\\n"
                            f"Min: {format_latency(data['min'])}\\n"
                            f"Max: {format_latency(data['max'])}\\n"
                            f"Avg: {format_latency(data['avg'])}\\n"
                            f"Median: {format_latency(data['median'])}"
                        )
                        
                        new_label = f'"{label_str}{stats_str}"'
                        subgraph.set_label(new_label)

        # Recursively process sub-subgraphs
        for sub in subgraph.get_subgraph_list():
            process_subgraph(sub)

    # Process the main graph's subgraphs
    for sub in graph.get_subgraph_list():
        process_subgraph(sub)

    # Generate output filename
    base, ext = os.path.splitext(dot_file_path)
    output_path = f"{base}_latency{ext}"
    
    try:
        graph.write_dot(output_path)
        print(f"\nSuccessfully created {output_path}")
    except Exception as e:
        print(f"Error writing output file: {e}")
        sys.exit(1)

def main():
    if len(sys.argv) < 2:
        print("Usage: python3 inject_latency.py <path_to_dot_file>")
        sys.exit(1)

    dot_file_path = sys.argv[1]
    log_file_path = "debug.log.ans"

    if not os.path.exists(dot_file_path):
        print(f"Error: DOT file '{dot_file_path}' not found.")
        sys.exit(1)
        
    print(f"Parsing log file: {log_file_path}...")
    latency_data = parse_log_file(log_file_path)
    print(f"Found latency data for {len(latency_data)} elements.\n")
    
    print_latency_table(latency_data)
    
    print(f"Processing DOT file: {dot_file_path}...")
    process_dot_file(dot_file_path, latency_data)

if __name__ == "__main__":
    main()