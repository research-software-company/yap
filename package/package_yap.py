# Create a package for YAP
import os
import shutil
import tempfile
import glob
import bz2
import platform
import stat

tmpdir = ""
yap_in_tmpdir = ""
yapdir = ""

def set_directories():
    global tmpdir, yapdir, yap_in_tmpdir
    tmpdir = tempfile.mkdtemp()
    yapdir = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
    yap_in_tmpdir = os.path.join(tmpdir, 'src', 'yap')
    print('Temporary package directory is ', tmpdir)

def set_go_path():
    os.environ['GOPATH'] = tmpdir

def copy_yap_content():
    print('Copying yap to temporary package directory')
    # We can't use shutil.copytree since it also copies the .git folder. Instead we 
    # copy each toplevel item explicitly
    os.makedirs(yap_in_tmpdir)

    for file in os.listdir(yapdir):
        if file[0] == '.' or file=='package':
            continue
        
        fullpath = os.path.join(yapdir, file)
        full_target = os.path.join(yap_in_tmpdir, file)
        if os.path.isfile(fullpath):
            shutil.copyfile(fullpath, full_target)
        else:
            shutil.copytree(fullpath, full_target)

def uncompress_data_files():
    def uncompress(bz2_file: str):
        b64_file = bz2_file[:-4]

        with open(b64_file, 'wb') as new_file, bz2.BZ2File(bz2_file, 'rb') as file:
            for data in iter(lambda : file.read(100 * 1024), b''):
                new_file.write(data)

    print('Uncompressing data files')
    datadir = os.path.join(yap_in_tmpdir, 'data')
    for bz2_file in glob.glob(os.path.join(datadir, '*.bz2')):
        uncompress(bz2_file)
        os.remove(bz2_file)

def go_build():
    os.chdir(yap_in_tmpdir)
    print('Running go get .')
    os.system('go get .')
    print('Running go build .')
    os.system('go build .')

def detect_os():
    system = platform.system()
    if system == 'Windows':
        return 'windows'
    if system == 'Darwin':
        return 'mac'
    raise ValueError('Unsupported platform %s, only Windows and Mac are supported' % system)

def add_script():
    print('Copying final files')
    platform = detect_os()
    if platform == 'windows':
        shutil.copyfile(os.path.join(yapdir, 'package', 'run-yap-api.bat'),
                        os.path.join(yap_in_tmpdir, 'run-yap-api.bat'))
    elif platform == 'mac':
        target_script = os.path.join(yap_in_tmpdir, 'run-yap-api.sh')
        shutil.copyfile(os.path.join(yapdir, 'package', 'run-yap-api.sh'), target_script)
        os.chmod(target_script, stat.S_IRWXU | stat.S_IRXG | stat.S_IRXO)
        

def zip_content():
    print('Creating pacakge')
    zip_filename = 'yap-%s' % detect_os()
    zip_pathname = os.path.join(yapdir, 'package', zip_filename)  # make_archive wants the name without an extension
    full_zip_pathname = zip_pathname + '.zip'
    if os.path.exists(full_zip_pathname):
        os.remove(full_zip_pathname)

    shutil.make_archive(zip_pathname, 'zip', yap_in_tmpdir)

    return zip_pathname + '.zip'

def run():
    global tmpdir

    print('Generating a package for YAP')
    set_directories()
    cwd = os.curdir

    try:
        set_go_path()
        copy_yap_content()
        uncompress_data_files()
        go_build()
        add_script()
        zip_file = zip_content()
        print('Package created in %s' % zip_file)

    finally:
        try:
            shutil.rmtree(tmpdir)
        except: 
            pass
        os.chdir(cwd)
    print("We're done")

if __name__=='__main__':
    run()
